/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxmoxnetworkcontroller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
)

// +kubebuilder:object:generate=false

// ZoneReconciler reports a zone's allocation state, checks its provider
// network, and collects Vlans whose network is gone.
type ZoneReconciler struct {
	client.Client
	APIReader client.Reader
	// SubnetPruneTimeout overrides DefaultSubnetPruneTimeout when set. It
	// should match NetworkReconciler.SubnetPruneTimeout.
	SubnetPruneTimeout time.Duration
}

// SetupWithManager registers the zone controller. It relies on the network
// controller's field indexes, so that controller must be set up first.
func (r *ZoneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pxv1.ProxmoxNetworkZone{}).
		Watches(&Vlan{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			if z := o.GetLabels()[pxv1.LabelZone]; z != "" {
				return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: z}}}
			}
			return nil
		})).
		Watches(&pxv1.ProxmoxNetwork{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: o.(*pxv1.ProxmoxNetwork).Spec.Zone}}}
		})).
		Watches(&ProviderNetwork{}, handler.EnqueueRequestsFromMapFunc(r.providerToZones)).
		Named("proxmoxnetworkzone").
		Complete(r)
}

func (r *ZoneReconciler) providerToZones(ctx context.Context, o client.Object) []reconcile.Request {
	var zones pxv1.ProxmoxNetworkZoneList
	if err := r.List(ctx, &zones); err != nil {
		log.FromContext(ctx).Error(err, "list zones for a provider network event")
		return nil
	}
	var out []reconcile.Request
	for _, z := range zones.Items {
		if z.Spec.ProviderNetwork == o.GetName() {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKey{Name: z.Name}})
		}
	}
	return out
}

// Reconcile computes zone status.
func (r *ZoneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	zone := &pxv1.ProxmoxNetworkZone{}
	if err := r.Get(ctx, req.NamespacedName, zone); err != nil {
		if apierrors.IsNotFound(err) {
			forgetZone(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	status := zone.Status.DeepCopy()
	status.ObservedGeneration = zone.Generation
	gen := zone.Generation
	setZoneCondition := func(s metav1.ConditionStatus, reason, msg string) {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type: pxv1.ConditionReady, Status: s, Reason: reason, Message: msg, ObservedGeneration: gen,
		})
	}

	candidates, err := allocatable(zone.Spec.VLANRanges, zone.Spec.ReservedVLANs)
	if err != nil {
		setZoneCondition(metav1.ConditionFalse, "ZoneInvalid", err.Error())
		return ctrl.Result{}, r.writeZoneStatus(ctx, zone, status)
	}

	var vlans VlanList
	if err := r.APIReader.List(ctx, &vlans); err != nil {
		return ctrl.Result{}, err
	}
	used := vlanSet{}
	inRange := vlanSet{}
	for _, id := range candidates {
		inRange.add(id)
	}
	static := vlanSet{}
	for _, s := range zone.Spec.StaticAssignments {
		static.add(s.VLAN)
	}

	var allocated int32
	var orphans int
	var stranded []string
	requeue := resyncPeriod
	for i := range vlans.Items {
		v := &vlans.Items[i]
		if v.Spec.Provider == zone.Spec.ProviderNetwork {
			used.add(int32(v.Spec.ID))
		}
		if v.Labels[pxv1.LabelZone] != zone.Name {
			continue
		}
		allocated++
		id := int32(v.Spec.ID)
		if !inRange.has(id) && !static.has(id) {
			stranded = append(stranded, fmt.Sprintf("%d (%s)", id, v.Name))
		}
		collected, recheck, err := r.collectOrphan(ctx, v)
		if err != nil {
			return ctrl.Result{}, err
		}
		if collected {
			allocated--
			continue
		}
		if recheck > 0 && recheck < requeue {
			requeue = recheck
		}
		if r.isOrphan(ctx, v) {
			orphans++
		}
	}

	var nets pxv1.ProxmoxNetworkList
	if err := r.List(ctx, &nets, client.MatchingFields{indexNetworkZone: zone.Name}); err != nil {
		return ctrl.Result{}, err
	}

	// A VLAN pinned by a static assignment, of this or another zone on the same
	// provider network, is not free for anyone else.
	var zones pxv1.ProxmoxNetworkZoneList
	if err := r.List(ctx, &zones); err != nil {
		return ctrl.Result{}, err
	}
	for _, z := range zones.Items {
		if z.Spec.ProviderNetwork == zone.Spec.ProviderNetwork {
			for _, sa := range z.Spec.StaticAssignments {
				used.add(sa.VLAN)
			}
		}
	}
	status.TotalVLANs = int32(len(candidates))
	status.AllocatedVLANs = allocated
	status.FreeVLANs = countFree(candidates, used)
	status.Networks = int32(len(nets.Items))

	zoneVLANsTotal.WithLabelValues(zone.Name).Set(float64(status.TotalVLANs))
	zoneVLANsAllocated.WithLabelValues(zone.Name).Set(float64(status.AllocatedVLANs))
	zoneVLANsFree.WithLabelValues(zone.Name).Set(float64(status.FreeVLANs))
	zoneNetworks.WithLabelValues(zone.Name).Set(float64(status.Networks))
	orphanedVLANs.WithLabelValues(zone.Name).Set(float64(orphans))

	pn := &ProviderNetwork{}
	switch err := r.Get(ctx, client.ObjectKey{Name: zone.Spec.ProviderNetwork}, pn); {
	case apierrors.IsNotFound(err):
		setZoneCondition(metav1.ConditionFalse, "ProviderNetworkNotFound",
			fmt.Sprintf("Kube-OVN ProviderNetwork %q does not exist", zone.Spec.ProviderNetwork))
	case err != nil:
		return ctrl.Result{}, err
	case !pn.Status.Ready:
		setZoneCondition(metav1.ConditionFalse, "ProviderNetworkNotReady",
			fmt.Sprintf("Kube-OVN ProviderNetwork %q is not ready on every node it should be", zone.Spec.ProviderNetwork))
	default:
		msg := fmt.Sprintf("%d of %d VLANs free; trunk ready on %d node(s)", status.FreeVLANs, status.TotalVLANs, len(pn.Status.ReadyNodes))
		if len(stranded) > 0 {
			sort.Strings(stranded)
			msg += "; allocated outside the current ranges: " + strings.Join(stranded, ", ")
		}
		if orphans > 0 {
			msg += fmt.Sprintf("; %d orphaned Vlan(s) still referenced by a subnet", orphans)
		}
		setZoneCondition(metav1.ConditionTrue, "Ready", msg)
	}
	if err := r.writeZoneStatus(ctx, zone, status); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// isOrphan reports whether a zone Vlan has lost its ProxmoxNetwork.
func (r *ZoneReconciler) isOrphan(ctx context.Context, v *Vlan) bool {
	ns, name := v.Labels[pxv1.LabelNetworkNamespace], v.Labels[pxv1.LabelNetworkName]
	if ns == "" || name == "" {
		return true
	}
	err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &pxv1.ProxmoxNetwork{})
	return apierrors.IsNotFound(err)
}

// collectOrphan deletes a zone Vlan whose ProxmoxNetwork is gone once no
// Kube-OVN subnet holds it any more. A network that went away the normal way
// already released its Vlan; this covers one whose finalizer was removed by
// hand. It reads the Vlan the way the network finalizer does: a subnet that
// still exists and is listed on the Vlan, or whose spec.vlan names it, holds
// it with no time limit; a deleted subnet still listed holds it until Kube-OVN
// prunes the name, or until its prune timeout runs out, the timer carried over
// from the network if it was waiting already. A held Vlan is left alone and
// only counted; recheck says when its prune timeout runs out, if that is what
// holds it.
func (r *ZoneReconciler) collectOrphan(ctx context.Context, v *Vlan) (collected bool, recheck time.Duration, err error) {
	if !r.isOrphan(ctx, v) {
		return false, 0, nil
	}
	live, gone, err := splitSubnets(ctx, r.APIReader, v.Status.Subnets)
	if err != nil {
		return false, 0, err
	}
	newest, err := trackDeletedSubnets(ctx, r.Client, v, gone, time.Now(), time.Time{})
	if err != nil {
		return false, 0, err
	}
	if len(live) > 0 {
		return false, 0, nil
	}
	var subnets SubnetList
	if err := r.List(ctx, &subnets); err != nil {
		return false, 0, err
	}
	for _, s := range subnets.Items {
		if s.Spec.Vlan == v.Name {
			return false, 0, nil
		}
	}
	if len(gone) > 0 {
		timeout := pruneTimeoutOrDefault(r.SubnetPruneTimeout)
		if left := time.Until(newest.Add(timeout)); left > 0 {
			return false, left, nil
		}
		log.FromContext(ctx).Info("Kube-OVN did not prune deleted subnets from the orphaned Vlan in time; collecting it anyway",
			"vlan", v.Spec.ID, "name", v.Name, "subnets", gone, "timeout", timeout)
	}
	if err := removeFinalizer(ctx, r.Client, v); err != nil {
		return false, 0, err
	}
	if err := r.Delete(ctx, v); client.IgnoreNotFound(err) != nil {
		return false, 0, err
	}
	log.FromContext(ctx).Info("collected orphaned VLAN", "vlan", v.Spec.ID, "name", v.Name)
	return true, 0, nil
}

func (r *ZoneReconciler) writeZoneStatus(ctx context.Context, zone *pxv1.ProxmoxNetworkZone, status *pxv1.ProxmoxNetworkZoneStatus) error {
	if equality.Semantic.DeepEqual(&zone.Status, status) {
		return nil
	}
	zone.Status = *status
	return r.Status().Update(ctx, zone)
}
