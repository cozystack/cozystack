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
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
)

const (
	indexNetworkZone   = "spec.zone"
	indexNetworkSubnet = "spec.subnet"
	indexNetworkPool   = "spec.ipPool"

	// ipamProtectPoolFinalizer is the in-cluster IPAM provider's finalizer. It
	// stays on a deleted pool until the provider has seen it empty.
	ipamProtectPoolFinalizer = "ipam.cluster.x-k8s.io/ProtectPool"

	requeueWhileInUse = 30 * time.Second
	resyncPeriod      = 10 * time.Minute

	// DefaultSubnetPruneTimeout bounds how long a deleted subnet that Kube-OVN
	// still lists in Vlan.status.subnets holds the Vlan, counted for each
	// subnet from when the controller first saw it gone. It applies to a
	// deleting network and to the zone's orphan collection alike. Kube-OVN
	// drops the name right after deleting the subnet's logical switch, from
	// an in-memory delete queue. A name still there after this long was lost
	// with a kube-ovn-controller restart: the next leader deletes the orphaned
	// switch in its startup garbage collection, which is the only place it
	// collects switches, and never prunes the name.
	DefaultSubnetPruneTimeout = 10 * time.Minute
)

var proxmoxMachineListGVK = schema.GroupVersionKind{Group: "infrastructure.cluster.x-k8s.io", Version: "v1alpha1", Kind: "ProxmoxMachineList"}

// +kubebuilder:object:generate=false

// NetworkReconciler maps a ProxmoxNetwork onto a Kube-OVN Vlan, reports
// whether the subnet and its IP pool form a usable Proxmox network, and holds
// the VLAN until the last VM address on it is released.
type NetworkReconciler struct {
	client.Client
	// APIReader reads Vlans past the informer cache. Allocation must see the
	// Vlan it created a moment ago, or two networks reconciled back to back
	// could be handed the same ID.
	APIReader client.Reader
	// SubnetPruneTimeout overrides DefaultSubnetPruneTimeout when set.
	SubnetPruneTimeout time.Duration
	// Gateways keeps the OVN HA chassis group of each network's router port
	// (gateway.go). Nil means the controller does not manage them: the
	// GatewayReady condition is then Unknown and Ready does not wait for it.
	Gateways *GatewayManager
	// now times the prune waits; the zero value is the wall clock. Tests
	// move it forward rather than move the timers back, because a network
	// only trusts timers that started after its own deletion.
	now clock
}

func (r *NetworkReconciler) subnetPruneTimeout() time.Duration {
	return pruneTimeoutOrDefault(r.SubnetPruneTimeout)
}

func pruneTimeoutOrDefault(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return DefaultSubnetPruneTimeout
}

// SetupWithManager registers the field indexes and watches. Allocation runs
// with a single worker: the used-set read and the Vlan create are not atomic,
// and serialising them is what makes the allocator race-free within one
// leader.
//
// The gateway step shares the worker: it reads the subnet and the zone the
// rest of the pass has just read, and its outcome is part of Ready, so one
// writer of the status keeps the conditions consistent with each other.
func (r *NetworkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := setupNetworkIndexes(context.Background(), mgr); err != nil {
		return err
	}
	b := ctrl.NewControllerManagedBy(mgr).
		For(&pxv1.ProxmoxNetwork{}).
		Watches(&Vlan{}, handler.EnqueueRequestsFromMapFunc(vlanToNetwork)).
		Watches(&Subnet{}, handler.EnqueueRequestsFromMapFunc(r.subnetToNetworks)).
		Watches(&InClusterIPPool{}, handler.EnqueueRequestsFromMapFunc(r.poolToNetworks)).
		Watches(&pxv1.ProxmoxNetworkZone{}, handler.EnqueueRequestsFromMapFunc(r.zoneToNetworks))
	if r.Gateways != nil {
		// Trunk nodes joining or leaving a provider network, or changing
		// chassis, change the groups of every network of its zones.
		b = b.Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.nodeToNetworks),
			builder.WithPredicates(nodeGatewayPredicate))
	}
	return b.
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Named("proxmoxnetwork").
		Complete(r)
}

func setupNetworkIndexes(ctx context.Context, mgr ctrl.Manager) error {
	idx := mgr.GetFieldIndexer()
	for field, fn := range map[string]func(*pxv1.ProxmoxNetwork) string{
		indexNetworkZone:   func(n *pxv1.ProxmoxNetwork) string { return n.Spec.Zone },
		indexNetworkSubnet: func(n *pxv1.ProxmoxNetwork) string { return n.Spec.Subnet },
		indexNetworkPool:   func(n *pxv1.ProxmoxNetwork) string { return n.Spec.IPPool },
	} {
		if err := idx.IndexField(ctx, &pxv1.ProxmoxNetwork{}, field, func(o client.Object) []string {
			return []string{fn(o.(*pxv1.ProxmoxNetwork))}
		}); err != nil {
			return fmt.Errorf("index %s: %w", field, err)
		}
	}
	return nil
}

func vlanToNetwork(_ context.Context, o client.Object) []reconcile.Request {
	l := o.GetLabels()
	ns, name := l[pxv1.LabelNetworkNamespace], l[pxv1.LabelNetworkName]
	if ns == "" || name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: ns, Name: name}}}
}

func (r *NetworkReconciler) networksMatching(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
	var list pxv1.ProxmoxNetworkList
	if err := r.List(ctx, &list, opts...); err != nil {
		log.FromContext(ctx).Error(err, "list ProxmoxNetworks for a watch event")
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return out
}

func (r *NetworkReconciler) subnetToNetworks(ctx context.Context, o client.Object) []reconcile.Request {
	return r.networksMatching(ctx, client.MatchingFields{indexNetworkSubnet: o.GetName()})
}

func (r *NetworkReconciler) poolToNetworks(ctx context.Context, o client.Object) []reconcile.Request {
	return r.networksMatching(ctx, client.InNamespace(o.GetNamespace()), client.MatchingFields{indexNetworkPool: o.GetName()})
}

func (r *NetworkReconciler) zoneToNetworks(ctx context.Context, o client.Object) []reconcile.Request {
	return r.networksMatching(ctx, client.MatchingFields{indexNetworkZone: o.GetName()})
}

// Reconcile drives one ProxmoxNetwork.
func (r *NetworkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	defer func() { reconcileDuration.Observe(time.Since(start).Seconds()) }()

	net := &pxv1.ProxmoxNetwork{}
	if err := r.Get(ctx, req.NamespacedName, net); err != nil {
		if apierrors.IsNotFound(err) {
			forgetNetwork(req.Namespace, req.Name)
			if r.Gateways != nil {
				r.Gateways.noteSuccess(networkKey(req.Namespace, req.Name))
			}
			return ctrl.Result{}, r.syncNamespaceAnnotation(ctx, req.Namespace)
		}
		return ctrl.Result{}, err
	}

	if !net.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, net)
	}

	if !controllerutil.ContainsFinalizer(net, pxv1.Finalizer) {
		patch := client.MergeFromWithOptions(net.DeepCopy(), client.MergeFromWithOptimisticLock{})
		controllerutil.AddFinalizer(net, pxv1.Finalizer)
		if err := r.Patch(ctx, net, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	status := net.Status.DeepCopy()
	status.ObservedGeneration = net.Generation
	if err := r.reconcileNetwork(ctx, net, status); err != nil {
		reconcileErrors.WithLabelValues("error").Inc()
		return ctrl.Result{}, err
	}

	ready := meta.FindStatusCondition(status.Conditions, pxv1.ConditionReady)
	if ready != nil && ready.Status != metav1.ConditionTrue {
		reconcileErrors.WithLabelValues(ready.Reason).Inc()
	}
	// The admission policy reads the namespace annotation, and charts render
	// machines as soon as the network reads Ready. So the network's triple
	// lands in the annotation before Ready lands in the status.
	if status.VLAN != 0 && status.Bridge != "" {
		if err := r.addToNamespaceAnnotation(ctx, net.Namespace, networkTriple(status.Bridge, status.VLAN, net.Spec.IPPool)); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.writeStatus(ctx, net, status); err != nil {
		return ctrl.Result{}, err
	}
	recordNetworkMetrics(net)
	if err := r.syncNamespaceAnnotation(ctx, net.Namespace); err != nil {
		return ctrl.Result{}, err
	}
	if r.Gateways == nil {
		return ctrl.Result{RequeueAfter: resyncPeriod}, nil
	}
	// A pass that could not reach the NB, and a router port Kube-OVN has
	// not created yet, are retried sooner than the resync.
	if r.Gateways.failing(networkKey(net.Namespace, net.Name)) {
		return ctrl.Result{RequeueAfter: gatewayRetryPeriod}, nil
	}
	if gw := meta.FindStatusCondition(status.Conditions, pxv1.ConditionGatewayReady); gw != nil &&
		(gw.Reason == pxv1.GatewayReasonNBUnavailable || gw.Reason == pxv1.GatewayReasonRouterPortMissing) {
		return ctrl.Result{RequeueAfter: gatewayRetryPeriod}, nil
	}
	// OVN NB has no watch here: the shorter resync repairs a group someone
	// cleared and a router port Kube-OVN recreated without one.
	return ctrl.Result{RequeueAfter: gatewayResyncPeriod}, nil
}

// reconcileNetwork fills status. A missing or misconfigured dependency is a
// condition, not an error: errors are only for API failures worth a retry with
// backoff.
func (r *NetworkReconciler) reconcileNetwork(ctx context.Context, net *pxv1.ProxmoxNetwork, st *pxv1.ProxmoxNetworkStatus) error {
	gen := net.Generation
	if r.Gateways == nil {
		setCondition(st, gen, pxv1.ConditionGatewayReady, metav1.ConditionUnknown, pxv1.GatewayReasonDisabled,
			"the controller does not manage the router port's HA chassis group (--manage-gateway-chassis=false)")
	} else if gw := meta.FindStatusCondition(st.Conditions, pxv1.ConditionGatewayReady); gw != nil && gw.Reason == pxv1.GatewayReasonDisabled {
		// Management was switched on: Disabled from an earlier pass must
		// not survive a pass that stops before the gateway step.
		meta.RemoveStatusCondition(&st.Conditions, pxv1.ConditionGatewayReady)
	}

	zone := &pxv1.ProxmoxNetworkZone{}
	if err := r.Get(ctx, client.ObjectKey{Name: net.Spec.Zone}, zone); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		setNotReady(st, gen, pxv1.ConditionVLANAllocated, "ZoneNotFound",
			fmt.Sprintf("ProxmoxNetworkZone %q does not exist", net.Spec.Zone))
		return nil
	}
	allowed, err := r.namespaceAllowed(ctx, zone, net.Namespace)
	if err != nil {
		return err
	}
	if !allowed {
		setNotReady(st, gen, pxv1.ConditionVLANAllocated, "NamespaceNotAllowed",
			fmt.Sprintf("namespace %q does not match the namespaceSelector of zone %q", net.Namespace, zone.Name))
		return nil
	}

	vlan, reason, msg, err := r.ensureVLAN(ctx, zone, net)
	if err != nil {
		return err
	}
	if vlan == nil {
		setNotReady(st, gen, pxv1.ConditionVLANAllocated, reason, msg)
		return nil
	}
	st.VLAN = int32(vlan.Spec.ID)
	st.VLANName = vlan.Name
	st.Bridge = zone.Spec.Bridge
	st.MTU = zone.Spec.MTU
	if vlan.Status.Conflict {
		setNotReady(st, gen, pxv1.ConditionVLANAllocated, "VLANConflict",
			fmt.Sprintf("Kube-OVN reports VLAN %d on provider %q as conflicting with another Vlan", vlan.Spec.ID, vlan.Spec.Provider))
		return nil
	}
	allocMsg := fmt.Sprintf("VLAN %d on bridge %s", vlan.Spec.ID, zone.Spec.Bridge)
	if s := staticAssignment(zone, net); s != nil && s.VLAN != int32(vlan.Spec.ID) {
		allocMsg += fmt.Sprintf("; the static assignment asks for VLAN %d, which applies only when the network is recreated", s.VLAN)
	}
	setCondition(st, gen, pxv1.ConditionVLANAllocated, metav1.ConditionTrue, "Allocated", allocMsg)

	subnet := &Subnet{}
	subnetBound := false
	if err := r.Get(ctx, client.ObjectKey{Name: net.Spec.Subnet}, subnet); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		setCondition(st, gen, pxv1.ConditionSubnetReady, metav1.ConditionFalse, "SubnetNotFound",
			fmt.Sprintf("Kube-OVN Subnet %q does not exist", net.Spec.Subnet))
	} else if subnet.Spec.Vlan != vlan.Name {
		setCondition(st, gen, pxv1.ConditionSubnetReady, metav1.ConditionFalse, "SubnetNotBound",
			fmt.Sprintf("Subnet %q has spec.vlan %q, expected %q", subnet.Name, subnet.Spec.Vlan, vlan.Name))
	} else {
		subnetBound = true
		if subnet.DeletionTimestamp.IsZero() {
			if err := addFinalizer(ctx, r.Client, subnet); err != nil {
				return err
			}
		}
		st.CIDR = subnet.Spec.CIDRBlock
		st.Gateway = subnet.Spec.Gateway
		if p, err := netip.ParsePrefix(subnet.Spec.CIDRBlock); err == nil {
			st.Prefix = int32(p.Bits())
		}
		if c := kubeOVNCondition(subnet.Status.Conditions, "Ready"); c != nil && c.Status == string(corev1.ConditionTrue) {
			setCondition(st, gen, pxv1.ConditionSubnetReady, metav1.ConditionTrue, "Ready", "Kube-OVN reports the subnet Ready")
		} else {
			reason, msg := "SubnetNotReady", "Kube-OVN has not reported the subnet Ready yet"
			if c != nil && c.Message != "" {
				msg = c.Message
			}
			if c := kubeOVNCondition(subnet.Status.Conditions, "Validated"); c != nil && c.Status == string(corev1.ConditionFalse) {
				reason, msg = "SubnetInvalid", c.Message
			}
			setCondition(st, gen, pxv1.ConditionSubnetReady, metav1.ConditionFalse, reason, msg)
		}
	}

	pool := &InClusterIPPool{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: net.Namespace, Name: net.Spec.IPPool}, pool); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		st.IPPool = nil
		setCondition(st, gen, pxv1.ConditionIPPoolReady, metav1.ConditionFalse, "PoolNotFound",
			fmt.Sprintf("InClusterIPPool %q does not exist in this namespace", net.Spec.IPPool))
	} else {
		if a := pool.Status.Addresses; a != nil {
			st.IPPool = &pxv1.IPPoolUsage{Total: int32(a.Total), Used: int32(a.Used), Free: int32(a.Free)}
		}
		switch {
		case !subnetBound:
			setCondition(st, gen, pxv1.ConditionIPPoolReady, metav1.ConditionUnknown, "SubnetUnavailable",
				"the pool is checked against the subnet once the subnet is bound")
		default:
			if reason, msg := checkPoolSplit(subnet.Spec.CIDRBlock, subnet.Spec.Gateway, subnet.Spec.ExcludeIps, pool.Spec); reason != "" {
				setCondition(st, gen, pxv1.ConditionIPPoolReady, metav1.ConditionFalse, reason, msg)
			} else if st.IPPool != nil && st.IPPool.Total > 0 && st.IPPool.Free == 0 {
				// Exhaustion does not make the network unusable for the VMs
				// already on it, so it stays Ready; the reason and the
				// metric carry the warning.
				setCondition(st, gen, pxv1.ConditionIPPoolReady, metav1.ConditionTrue, "PoolExhausted",
					fmt.Sprintf("all %d addresses of the pool are allocated; new machines will wait", st.IPPool.Total))
			} else {
				setCondition(st, gen, pxv1.ConditionIPPoolReady, metav1.ConditionTrue, "Ready", "pool lies inside the subnet's excludeIps")
			}
		}
	}

	if r.Gateways != nil {
		var bound *Subnet
		if subnetBound {
			bound = subnet
		}
		if err := r.reconcileGateway(ctx, zone, net, bound, st); err != nil {
			return err
		}
	}

	summarizeReady(st, gen)
	return nil
}

// reconcileGateway gives the subnet's router port an HA chassis group of the
// zone's trunk nodes and records the outcome as GatewayReady. An unreachable
// OVN NB is a condition, retried sooner than the resync (Reconcile).
func (r *NetworkReconciler) reconcileGateway(ctx context.Context, zone *pxv1.ProxmoxNetworkZone, net *pxv1.ProxmoxNetwork, subnet *Subnet, st *pxv1.ProxmoxNetworkStatus) error {
	gen := net.Generation
	key := networkKey(net.Namespace, net.Name)
	set := func(s metav1.ConditionStatus, reason, msg string) {
		setCondition(st, gen, pxv1.ConditionGatewayReady, s, reason, msg)
	}
	if subnet == nil {
		set(metav1.ConditionUnknown, "SubnetUnavailable", "the router port is looked up once the subnet is bound")
		return nil
	}
	if subnet.Spec.Vpc == "" {
		set(metav1.ConditionFalse, pxv1.GatewayReasonRouterPortMissing,
			fmt.Sprintf("Subnet %q has no spec.vpc yet, so its router port is unknown", subnet.Name))
		networkGatewayChassis.WithLabelValues(net.Namespace, net.Name).Set(0)
		return nil
	}
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes, client.MatchingLabels{providerReadyLabel(zone.Spec.ProviderNetwork): "true"}); err != nil {
		return err
	}
	lrp := routerPortName(subnet.Spec.Vpc, subnet.Name)
	nbCtx, cancel := r.Gateways.withPassTimeout(ctx)
	defer cancel()
	gw, err := r.Gateways.ensure(nbCtx, lrp, key, gatewayChassisFor(nodes.Items, gatewayKeyForVPC(subnet.Spec.Vpc)))
	if err != nil {
		r.gatewayFailed(ctx, net, st, lrp, err)
		return nil
	}
	r.Gateways.noteSuccess(key)
	networkGatewayChassis.WithLabelValues(net.Namespace, net.Name).Set(float64(gw.Chassis))
	if gw.Reason == pxv1.GatewayReasonChassisAssigned {
		set(metav1.ConditionTrue, gw.Reason, gw.Message)
	} else {
		set(metav1.ConditionFalse, gw.Reason, gw.Message)
	}
	return nil
}

// gatewayFailed records a pass that could not bring the group up to date: an
// unreachable NB, a TLS failure, or a row that changed under the controller.
//
// None of these says the group is gone. A network whose gateway was up keeps
// its GatewayReady, and so its Ready, for gatewayNBGracePeriod from the first
// failure: as True when it was True, as Unknown/NBUnavailable when it was not
// known yet but the network was Ready (management just switched on, as on an
// upgrade). Only then, or straight away for a network whose gateway was never
// up, it turns False/NBUnavailable. The run of failures is timed in memory,
// and from the condition's lastTransitionTime once it is Unknown, so a
// restart does not start the grace again for those.
func (r *NetworkReconciler) gatewayFailed(ctx context.Context, net *pxv1.ProxmoxNetwork, st *pxv1.ProxmoxNetworkStatus, lrp string, err error) {
	gen := net.Generation
	now := r.now.Now()
	gatewayNBErrors.Inc()
	since := r.Gateways.noteFailure(networkKey(net.Namespace, net.Name), now)
	prev := meta.FindStatusCondition(net.Status.Conditions, pxv1.ConditionGatewayReady)
	wasUp := false
	switch {
	case prev != nil && prev.Status == metav1.ConditionTrue && prev.Reason == pxv1.GatewayReasonChassisAssigned:
		wasUp = true
	case prev != nil && prev.Status == metav1.ConditionUnknown && prev.Reason == pxv1.GatewayReasonNBUnavailable:
		wasUp = true
		if t := prev.LastTransitionTime.Time; !t.IsZero() && t.Before(since) {
			since = t
		}
	case prev == nil || prev.Reason == pxv1.GatewayReasonDisabled:
		wasUp = meta.IsStatusConditionTrue(net.Status.Conditions, pxv1.ConditionReady)
	}
	deadline := since.Add(gatewayNBGracePeriod)
	log.FromContext(ctx).Error(err, "could not bring the OVN HA chassis group up to date", "routerPort", lrp,
		"failingSince", since, "keptUntil", deadline, "kept", wasUp && now.Before(deadline))
	if wasUp && now.Before(deadline) {
		if prev != nil && prev.Status == metav1.ConditionTrue {
			setCondition(st, gen, pxv1.ConditionGatewayReady, prev.Status, prev.Reason, prev.Message)
			return
		}
		setCondition(st, gen, pxv1.ConditionGatewayReady, metav1.ConditionUnknown, pxv1.GatewayReasonNBUnavailable,
			fmt.Sprintf("OVN NB has not answered since %s, so the HA chassis group of router port %q is not known; the network stays Ready until %s: %v",
				since.UTC().Format(time.RFC3339), lrp, deadline.UTC().Format(time.RFC3339), err))
		return
	}
	setCondition(st, gen, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable,
		fmt.Sprintf("could not bring the HA chassis group of router port %q up to date in OVN NB since %s: %v",
			lrp, since.UTC().Format(time.RFC3339), err))
}

// ensureVLAN adopts the Vlan named after the subnet or allocates a new one. It
// returns a nil Vlan with a reason when the network cannot have one.
func (r *NetworkReconciler) ensureVLAN(ctx context.Context, zone *pxv1.ProxmoxNetworkZone, net *pxv1.ProxmoxNetwork) (*Vlan, string, string, error) {
	name := net.Spec.Subnet
	existing := &Vlan{}
	err := r.APIReader.Get(ctx, client.ObjectKey{Name: name}, existing)
	switch {
	case err == nil:
		if !vlanOwnedBy(existing, net) {
			return nil, "VLANNameTaken", fmt.Sprintf("Kube-OVN Vlan %q exists and belongs to %s/%s",
				name, existing.Labels[pxv1.LabelNetworkNamespace], existing.Labels[pxv1.LabelNetworkName]), nil
		}
		if existing.Spec.Provider != zone.Spec.ProviderNetwork {
			return nil, "VLANProviderMismatch", fmt.Sprintf("Vlan %q uses provider %q, zone %q uses %q",
				name, existing.Spec.Provider, zone.Name, zone.Spec.ProviderNetwork), nil
		}
		if err := adoptVlan(ctx, r.Client, existing); err != nil {
			return nil, "", "", err
		}
		return existing, "", "", nil
	case !apierrors.IsNotFound(err):
		return nil, "", "", err
	}

	candidates, err := allocatable(zone.Spec.VLANRanges, zone.Spec.ReservedVLANs)
	if err != nil {
		return nil, "ZoneInvalid", err.Error(), nil
	}
	used, err := r.usedVLANs(ctx, zone.Spec.ProviderNetwork)
	if err != nil {
		return nil, "", "", err
	}

	var id int32
	if s := staticAssignment(zone, net); s != nil {
		for _, res := range zone.Spec.ReservedVLANs {
			if res == s.VLAN {
				vlanAllocations.WithLabelValues(zone.Name, "conflict").Inc()
				return nil, "StaticVLANReserved", fmt.Sprintf("static VLAN %d is reserved in zone %q", s.VLAN, zone.Name), nil
			}
		}
		if used.has(s.VLAN) {
			vlanAllocations.WithLabelValues(zone.Name, "conflict").Inc()
			return nil, "StaticVLANInUse", fmt.Sprintf("static VLAN %d is already used on provider %q", s.VLAN, zone.Spec.ProviderNetwork), nil
		}
		id = s.VLAN
	} else {
		// A VLAN pinned to some network by a static assignment is never handed
		// to another one, even before its owner exists. Pins of every zone on
		// the same provider network count, because they share its VLAN space.
		pinned, err := r.pinnedVLANs(ctx, zone.Spec.ProviderNetwork)
		if err != nil {
			return nil, "", "", err
		}
		for v := range pinned {
			used.add(v)
		}
		id, err = firstFree(candidates, used)
		if err != nil {
			vlanAllocations.WithLabelValues(zone.Name, "exhausted").Inc()
			return nil, "ZoneExhausted", fmt.Sprintf("zone %q has no free VLAN in %s", zone.Name, strings.Join(zone.Spec.VLANRanges, ",")), nil
		}
	}

	v := &Vlan{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				pxv1.LabelZone:             zone.Name,
				pxv1.LabelNetworkNamespace: net.Namespace,
				pxv1.LabelNetworkName:      net.Name,
			},
			Finalizers: []string{pxv1.Finalizer},
		},
		Spec: VlanSpec{ID: int(id), Provider: zone.Spec.ProviderNetwork},
	}
	if err := r.Create(ctx, v); err != nil {
		// AlreadyExists means a Vlan of that name appeared between the read
		// and the create; the retry adopts or rejects it.
		return nil, "", "", err
	}
	vlanAllocations.WithLabelValues(zone.Name, "allocated").Inc()
	log.FromContext(ctx).Info("allocated VLAN", "zone", zone.Name, "vlan", id, "subnet", name)
	return v, "", "", nil
}

// pinnedVLANs is every VLAN a static assignment of any zone on the provider
// network reserves.
func (r *NetworkReconciler) pinnedVLANs(ctx context.Context, provider string) (vlanSet, error) {
	var zones pxv1.ProxmoxNetworkZoneList
	if err := r.List(ctx, &zones); err != nil {
		return nil, err
	}
	out := vlanSet{}
	for _, z := range zones.Items {
		if z.Spec.ProviderNetwork != provider {
			continue
		}
		for _, s := range z.Spec.StaticAssignments {
			out.add(s.VLAN)
		}
	}
	return out, nil
}

// usedVLANs is every VLAN ID already on the provider network, whoever created
// it: a hand-made Vlan blocks its ID as much as an allocated one does.
func (r *NetworkReconciler) usedVLANs(ctx context.Context, provider string) (vlanSet, error) {
	var list VlanList
	if err := r.APIReader.List(ctx, &list); err != nil {
		return nil, err
	}
	used := vlanSet{}
	for _, v := range list.Items {
		if v.Spec.Provider == provider {
			used.add(int32(v.Spec.ID))
		}
	}
	return used, nil
}

func (r *NetworkReconciler) namespaceAllowed(ctx context.Context, zone *pxv1.ProxmoxNetworkZone, namespace string) (bool, error) {
	if zone.Spec.NamespaceSelector == nil {
		return true, nil
	}
	sel, err := metav1.LabelSelectorAsSelector(zone.Spec.NamespaceSelector)
	if err != nil {
		return false, nil
	}
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return sel.Matches(labels.Set(ns.Labels)), nil
}

// finalize releases the network once no VM sits on it any more, and releases
// the VLAN only once Kube-OVN has deleted the subnet and its logical switch.
// Kube-OVN validates a subnet's Vlan on every pass, the deleting one included,
// and removes its own finalizer only after that validation; a Vlan deleted
// first leaves the subnet Terminating for good. And the switch's localnet port
// carries the tag until Kube-OVN deletes the switch, which it does only after
// the Subnet object is gone; a VLAN released before that could be handed to
// another network while the old switch still sits on it.
func (r *NetworkReconciler) finalize(ctx context.Context, net *pxv1.ProxmoxNetwork) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(net, pxv1.Finalizer) {
		return ctrl.Result{}, nil
	}
	gen := net.Generation
	status := net.Status.DeepCopy()
	// SwitchDeleted is shown only on passes that find deleted subnets still
	// listed on the Vlan; any other outcome drops it. The prune timers
	// themselves live on the Vlan (trackDeletedSubnets).
	meta.RemoveStatusCondition(&status.Conditions, pxv1.ConditionSwitchDeleted)
	wait := func(reason, msg string, after time.Duration) (ctrl.Result, error) {
		networkDeleting.WithLabelValues(net.Namespace, net.Name).Set(1)
		setCondition(status, gen, pxv1.ConditionReady, metav1.ConditionFalse, reason, msg)
		if err := r.writeStatus(ctx, net, status); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: after}, nil
	}

	used, machines, inUse, err := r.inUse(ctx, net)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inUse {
		return wait("InUse", fmt.Sprintf("%d address(es) of pool %q are allocated and %d ProxmoxMachine(s) reference it; VLAN %d and subnet %q are kept until they are gone",
			used, net.Spec.IPPool, machines, net.Status.VLAN, net.Spec.Subnet), requeueWhileInUse)
	}

	vlanName := net.Spec.Subnet
	subnet := &Subnet{}
	switch err := r.APIReader.Get(ctx, client.ObjectKey{Name: net.Spec.Subnet}, subnet); {
	case err == nil:
		if subnet.DeletionTimestamp.IsZero() {
			// The subnet outlives the network. Its localnet port may still
			// carry this VLAN's tag whatever its spec.vlan says now (see the
			// Vlan check below), and nothing here can see the port, so the
			// VLAN is not free until the subnet is deleted.
			return wait("SubnetStillExists", fmt.Sprintf("Subnet %q still exists; VLAN %d is released after it is deleted", subnet.Name, net.Status.VLAN), requeueWhileInUse)
		}
		if err := removeFinalizer(ctx, r.Client, subnet); err != nil {
			return ctrl.Result{}, err
		}
		return wait("SubnetDeleting", fmt.Sprintf("waiting for Kube-OVN to delete Subnet %q before VLAN %d is released", subnet.Name, net.Status.VLAN), 10*time.Second)
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}

	// The subnet is gone, and with it, normally, its router port. The HA
	// chassis group goes before the VLAN is released, after clearing it from
	// any router port that still points at it. An NB that cannot be reached
	// holds the release: the group would otherwise only go with the periodic
	// collection, and Kube-OVN, which deletes the switch, needs the NB too.
	// A network that never had a VLAN never reached the gateway step, so it
	// has no group to wait for; the collection is the backstop should one
	// have been written after all.
	if r.Gateways != nil && net.Status.VLAN != 0 {
		nbCtx, cancel := r.Gateways.withPassTimeout(ctx)
		err := r.Gateways.release(nbCtx, networkKey(net.Namespace, net.Name))
		cancel()
		if err != nil {
			gatewayNBErrors.Inc()
			return wait("GatewayReleasePending", fmt.Sprintf("could not delete the network's HA chassis group from OVN NB: %v", err), gatewayRetryPeriod)
		}
	}

	vlan := &Vlan{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: vlanName}, vlan); err == nil {
		if vlanOwnedBy(vlan, net) {
			// Kube-OVN lists a subnet here until it has deleted the subnet's
			// logical switch, localnet port included.
			//
			// A live subnet holds the VLAN with no time limit, even when its
			// spec.vlan no longer names this Vlan. Kube-OVN sets the localnet
			// port's tag when it creates the port, and changes it only from
			// the update handler of the Vlan that spec.vlan names
			// (handleUpdateVlan -> setLocalnetTag). So the port keeps this
			// VLAN's tag when spec.vlan is cleared or names a missing Vlan.
			// When spec.vlan names another existing Vlan, reconcileVlan adds
			// the subnet to that Vlan's status, which fires its update handler,
			// and the port is retagged; this Vlan still lists the subnet,
			// because Kube-OVN prunes the list only on delete. Nothing here can
			// see the port's tag or tell a retag that ran from one that failed
			// or never fired, so the VLAN is held until such a subnet is
			// deleted (VLANStillReferenced). That errs on the safe side, and
			// only a subnet made or edited by hand can cause it: the vpc chart
			// names each Vlan after its own subnet.
			//
			// A deleted subnet is a delete Kube-OVN is still working through,
			// or one whose event a restart lost; the next leader then deletes
			// the switch but never prunes the name. So each deleted subnet
			// holds the VLAN until Kube-OVN prunes it, or until its own prune
			// timeout runs out. A timer that started before this network was
			// deleted is not this network's: an earlier network of the same
			// name, or the orphan collection before this one existed, left it
			// on a Vlan this network never adopted (adoptVlan drops them). It
			// starts afresh. A controller clock behind the API server's only
			// restarts the network's own timers until it catches up with the
			// deletion time, which delays the release by that skew.
			live, gone, err := splitSubnets(ctx, r.APIReader, vlan.Status.Subnets)
			if err != nil {
				return ctrl.Result{}, err
			}
			now := r.now.Now()
			newest, err := trackDeletedSubnets(ctx, r.Client, vlan, gone, now, net.DeletionTimestamp.Time)
			if err != nil {
				return ctrl.Result{}, err
			}
			if len(gone) > 0 {
				setSwitchDeleted(net, status, gone)
			}
			if len(live) > 0 {
				return wait("VLANStillReferenced", fmt.Sprintf("Kube-OVN still lists subnets %v on Vlan %q", live, vlanName), 10*time.Second)
			}
			if len(gone) > 0 {
				deadline := newest.Add(r.subnetPruneTimeout())
				if left := deadline.Sub(now); left > 0 {
					return wait("SwitchDeleting", fmt.Sprintf("Kube-OVN still lists deleted subnets %v on Vlan %q until it has deleted their logical switches; VLAN %d is released then, or at %s at the latest",
						gone, vlanName, vlan.Spec.ID, deadline.UTC().Format(time.RFC3339)), min(left, 10*time.Second))
				}
				log.FromContext(ctx).Info("Kube-OVN did not prune deleted subnets from the Vlan in time; releasing the VLAN anyway",
					"vlan", vlan.Spec.ID, "subnets", gone, "timeout", r.subnetPruneTimeout())
			}
			if err := removeFinalizer(ctx, r.Client, vlan); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.Delete(ctx, vlan); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			log.FromContext(ctx).Info("released VLAN", "zone", net.Spec.Zone, "vlan", vlan.Spec.ID, "subnet", vlanName)
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if err := removeFinalizer(ctx, r.Client, net); err != nil {
		return ctrl.Result{}, err
	}
	forgetNetwork(net.Namespace, net.Name)
	if r.Gateways != nil {
		r.Gateways.noteSuccess(networkKey(net.Namespace, net.Name))
	}
	return ctrl.Result{}, nil
}

// setSwitchDeleted records on st that Kube-OVN still lists deleted subnets on
// the Vlan. The condition's lastTransitionTime, carried over from the last
// written status, marks when the network began to wait for them; the timeout
// runs per subnet, from the timers trackDeletedSubnets keeps on the Vlan.
func setSwitchDeleted(net *pxv1.ProxmoxNetwork, st *pxv1.ProxmoxNetworkStatus, gone []string) {
	c := metav1.Condition{
		Type: pxv1.ConditionSwitchDeleted, Status: metav1.ConditionFalse, Reason: "ListedOnVlan",
		Message:            fmt.Sprintf("Kube-OVN still lists deleted subnets %v on the Vlan", gone),
		ObservedGeneration: net.Generation,
	}
	if prev := meta.FindStatusCondition(net.Status.Conditions, pxv1.ConditionSwitchDeleted); prev != nil && prev.Status == metav1.ConditionFalse {
		c.LastTransitionTime = prev.LastTransitionTime
	}
	meta.SetStatusCondition(&st.Conditions, c)
}

// inUse reports whether a VM may still sit on the network. Two independent
// signals are needed. The pool counts allocated addresses, but the IPAM
// provider releases an address as soon as its claim is deleted, which a
// namespace deletion does while capmox is still destroying the VM. So
// ProxmoxMachines that reference the pool, Terminating ones included, count
// too. A deleted pool that still carries the provider's finalizer counts as
// in use: the provider removes it only once it has seen the pool empty.
func (r *NetworkReconciler) inUse(ctx context.Context, net *pxv1.ProxmoxNetwork) (used, machines int, busy bool, err error) {
	pool := &InClusterIPPool{}
	switch err := r.Get(ctx, client.ObjectKey{Namespace: net.Namespace, Name: net.Spec.IPPool}, pool); {
	case err == nil:
		if pool.Status.Addresses != nil {
			used = pool.Status.Addresses.Used
		}
		if used > 0 || (!pool.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(pool, ipamProtectPoolFinalizer)) {
			busy = true
		}
	case !apierrors.IsNotFound(err):
		return 0, 0, false, err
	}
	machines, err = r.machinesOnPool(ctx, net.Namespace, net.Spec.IPPool)
	if err != nil {
		return 0, 0, false, err
	}
	return used, machines, busy || machines > 0, nil
}

// machinesOnPool counts ProxmoxMachines in the namespace whose NICs draw from
// the pool. It reads past the cache: a machine that just went away must not
// keep the network, and one that just appeared must not be missed.
func (r *NetworkReconciler) machinesOnPool(ctx context.Context, namespace, pool string) (int, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(proxmoxMachineListGVK)
	if err := r.APIReader.List(ctx, list, client.InNamespace(namespace)); err != nil {
		if meta.IsNoMatchError(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, m := range list.Items {
		if machineUsesPool(m.Object, pool) {
			n++
		}
	}
	return n, nil
}

func machineUsesPool(obj map[string]any, pool string) bool {
	refs := []map[string]any{}
	if d, ok, _ := unstructured.NestedMap(obj, "spec", "network", "default", "ipv4PoolRef"); ok {
		refs = append(refs, d)
	}
	if devs, ok, _ := unstructured.NestedSlice(obj, "spec", "network", "additionalDevices"); ok {
		for _, dev := range devs {
			if m, ok := dev.(map[string]any); ok {
				if ref, ok, _ := unstructured.NestedMap(m, "ipv4PoolRef"); ok {
					refs = append(refs, ref)
				}
			}
		}
	}
	for _, ref := range refs {
		if ref["kind"] == "InClusterIPPool" && ref["name"] == pool {
			return true
		}
	}
	return false
}

// syncNamespaceAnnotation publishes the namespace's allocated networks for the
// admission policy. Networks that are being deleted stay listed until they are
// gone: their machines still exist and capmox still updates them.
func (r *NetworkReconciler) syncNamespaceAnnotation(ctx context.Context, namespace string) error {
	var list pxv1.ProxmoxNetworkList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return err
	}
	entries := make([]string, 0, len(list.Items))
	for _, n := range list.Items {
		if n.Status.VLAN == 0 || n.Status.Bridge == "" {
			continue
		}
		if !n.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(&n, pxv1.Finalizer) {
			continue
		}
		entries = append(entries, networkTriple(n.Status.Bridge, n.Status.VLAN, n.Spec.IPPool))
	}
	sort.Strings(entries)
	want := strings.Join(entries, ",")

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return client.IgnoreNotFound(err)
	}
	if ns.Annotations[pxv1.AnnotationNamespaceNetworks] == want {
		return nil
	}
	patch := client.MergeFrom(ns.DeepCopy())
	if want == "" {
		delete(ns.Annotations, pxv1.AnnotationNamespaceNetworks)
	} else {
		if ns.Annotations == nil {
			ns.Annotations = map[string]string{}
		}
		ns.Annotations[pxv1.AnnotationNamespaceNetworks] = want
	}
	return r.Patch(ctx, ns, patch)
}

func networkTriple(bridge string, vlan int32, pool string) string {
	return bridge + "/" + strconv.Itoa(int(vlan)) + "/" + pool
}

// addToNamespaceAnnotation adds one triple to the namespace annotation if it is
// missing; the full sync after the status write removes stale ones.
func (r *NetworkReconciler) addToNamespaceAnnotation(ctx context.Context, namespace, triple string) error {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return client.IgnoreNotFound(err)
	}
	cur := ns.Annotations[pxv1.AnnotationNamespaceNetworks]
	entries := []string{}
	if cur != "" {
		entries = strings.Split(cur, ",")
	}
	for _, e := range entries {
		if e == triple {
			return nil
		}
	}
	entries = append(entries, triple)
	sort.Strings(entries)
	patch := client.MergeFrom(ns.DeepCopy())
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Annotations[pxv1.AnnotationNamespaceNetworks] = strings.Join(entries, ",")
	return r.Patch(ctx, ns, patch)
}

func (r *NetworkReconciler) writeStatus(ctx context.Context, net *pxv1.ProxmoxNetwork, status *pxv1.ProxmoxNetworkStatus) error {
	if equality.Semantic.DeepEqual(&net.Status, status) {
		return nil
	}
	net.Status = *status
	return r.Status().Update(ctx, net)
}

func recordNetworkMetrics(net *pxv1.ProxmoxNetwork) {
	networkReady.DeletePartialMatch(map[string]string{"namespace": net.Namespace, "network": net.Name})
	ready := 0.0
	if meta.IsStatusConditionTrue(net.Status.Conditions, pxv1.ConditionReady) {
		ready = 1
	}
	networkReady.WithLabelValues(net.Namespace, net.Name, net.Spec.Zone, strconv.Itoa(int(net.Status.VLAN))).Set(ready)
	networkDeleting.WithLabelValues(net.Namespace, net.Name).Set(0)
	if gw := meta.FindStatusCondition(net.Status.Conditions, pxv1.ConditionGatewayReady); gw == nil || gw.Reason == pxv1.GatewayReasonDisabled {
		networkGatewayReady.DeleteLabelValues(net.Namespace, net.Name)
		networkGatewayChassis.DeleteLabelValues(net.Namespace, net.Name)
	} else {
		ready := 0.0
		if gw.Status == metav1.ConditionTrue {
			ready = 1
		}
		networkGatewayReady.WithLabelValues(net.Namespace, net.Name).Set(ready)
	}
	if p := net.Status.IPPool; p != nil {
		networkPoolAddresses.WithLabelValues(net.Namespace, net.Name, "total").Set(float64(p.Total))
		networkPoolAddresses.WithLabelValues(net.Namespace, net.Name, "used").Set(float64(p.Used))
		networkPoolAddresses.WithLabelValues(net.Namespace, net.Name, "free").Set(float64(p.Free))
		networkConsumers.WithLabelValues(net.Namespace, net.Name).Set(float64(p.Used))
	}
}

func staticAssignment(zone *pxv1.ProxmoxNetworkZone, net *pxv1.ProxmoxNetwork) *pxv1.StaticVLANAssignment {
	for i := range zone.Spec.StaticAssignments {
		s := &zone.Spec.StaticAssignments[i]
		if s.Namespace == net.Namespace && s.Name == net.Name {
			return s
		}
	}
	return nil
}

func vlanOwnedBy(v *Vlan, net *pxv1.ProxmoxNetwork) bool {
	return v.Labels[pxv1.LabelNetworkNamespace] == net.Namespace && v.Labels[pxv1.LabelNetworkName] == net.Name
}

func kubeOVNCondition(conds []KubeOVNCondition, t string) *KubeOVNCondition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

func setCondition(st *pxv1.ProxmoxNetworkStatus, gen int64, t string, s metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type: t, Status: s, Reason: reason, Message: msg, ObservedGeneration: gen,
	})
}

// setNotReady marks one condition false and everything downstream of it
// unknown, so a stale True from an earlier pass cannot linger.
func setNotReady(st *pxv1.ProxmoxNetworkStatus, gen int64, t, reason, msg string) {
	setCondition(st, gen, t, metav1.ConditionFalse, reason, msg)
	downstream := []string{pxv1.ConditionVLANAllocated, pxv1.ConditionSubnetReady, pxv1.ConditionIPPoolReady}
	if gatewayManaged(st) {
		downstream = append(downstream, pxv1.ConditionGatewayReady)
	}
	for _, other := range downstream {
		if other != t && t == pxv1.ConditionVLANAllocated {
			setCondition(st, gen, other, metav1.ConditionUnknown, "WaitingForVLAN", "waits for a VLAN to be allocated")
		}
	}
	summarizeReady(st, gen)
}

// gatewayManaged is false only when the pass marked GatewayReady Disabled.
func gatewayManaged(st *pxv1.ProxmoxNetworkStatus) bool {
	gw := meta.FindStatusCondition(st.Conditions, pxv1.ConditionGatewayReady)
	return gw == nil || gw.Reason != pxv1.GatewayReasonDisabled
}

// summarizeReady sets Ready from the other conditions, GatewayReady included
// while the controller manages gateways: without the group, VMs on the VLAN
// get no ARP answer from their gateway. GatewayReady Unknown/NBUnavailable
// does not hold Ready: it is the grace of a network that was Ready while the
// NB cannot tell whether its group is there (gatewayFailed).
func summarizeReady(st *pxv1.ProxmoxNetworkStatus, gen int64) {
	required := []string{pxv1.ConditionVLANAllocated, pxv1.ConditionSubnetReady, pxv1.ConditionIPPoolReady}
	if gatewayManaged(st) {
		required = append(required, pxv1.ConditionGatewayReady)
	}
	for _, t := range required {
		c := meta.FindStatusCondition(st.Conditions, t)
		if t == pxv1.ConditionGatewayReady && c != nil && c.Status == metav1.ConditionUnknown && c.Reason == pxv1.GatewayReasonNBUnavailable {
			continue
		}
		if c == nil || c.Status != metav1.ConditionTrue {
			reason, msg := "Pending", t+" has not been evaluated"
			if c != nil {
				reason, msg = c.Reason, c.Message
			}
			setCondition(st, gen, pxv1.ConditionReady, metav1.ConditionFalse, reason, msg)
			return
		}
	}
	setCondition(st, gen, pxv1.ConditionReady, metav1.ConditionTrue, "Ready", "the subnet is usable from Proxmox")
}

func addFinalizer(ctx context.Context, c client.Client, o client.Object) error {
	if controllerutil.ContainsFinalizer(o, pxv1.Finalizer) {
		return nil
	}
	before, ok := o.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("deep copy of %T is not a client.Object", o)
	}
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	controllerutil.AddFinalizer(o, pxv1.Finalizer)
	return c.Patch(ctx, o, patch)
}

func removeFinalizer(ctx context.Context, c client.Client, o client.Object) error {
	if !controllerutil.ContainsFinalizer(o, pxv1.Finalizer) {
		return nil
	}
	before, ok := o.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("deep copy of %T is not a client.Object", o)
	}
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	controllerutil.RemoveFinalizer(o, pxv1.Finalizer)
	return client.IgnoreNotFound(c.Patch(ctx, o, patch))
}
