/*
Copyright 2026 The Cozystack Authors.

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

package endpointattachment

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
)

// A VM whose launcher pod cozyplane attaches to a VPC has only its VPC
// address: the pod's own IP, which a cozy-proxy Service would map to, never
// reaches the guest. cozyplane binds a public address to a VPC workload
// through its FloatingIP, which owns its own LoadBalancer Service and wears
// the attachment's claim there. The kind is read as unstructured and used only
// when the cluster serves it, so this controller carries no dependency on
// cozyplane.
const (
	// vpcAnnotation is the pod annotation through which the VM chart asks
	// cozyplane for a VPC attachment: "<vpc>" in the pod's namespace, or
	// "<owner-ns>/<vpc>".
	vpcAnnotation = "sdn.cozystack.io/vpc"
	// vpcIPAnnotation is where cozyplane's CNI copies the pod's VPC address
	// once it has attached the pod.
	vpcIPAnnotation = "sdn.cozystack.io/vpc-ip"

	floatingIPPhaseReady = "Ready"

	// endpointAnnotation records, on a FloatingIP this controller owns, the
	// endpoint Service it was bound for. A stopped VM has no pod to say which
	// endpoint a binding serves, so it is read from here.
	endpointAnnotation = "cozystack.io/endpoint-attachment-endpoint"
)

var floatingIPGVK = schema.GroupVersion{Group: "sdn.cozystack.io", Version: "v1alpha1"}.WithKind("FloatingIP")

func newFloatingIP() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(floatingIPGVK)
	return u
}

// vpcTarget is the one thing a FloatingIP binds: an address inside a VPC.
type vpcTarget struct {
	VPC string
	IP  string
}

// vpcBackends describes how an endpoint's backend pods sit in cozyplane VPCs.
type vpcBackends struct {
	// InVPC is true when any backend pod asks for a VPC attachment.
	InVPC bool
	// Pods counts the backend pods considered.
	Pods int
	// Targets are the distinct VPC addresses the backends hold. A live
	// migration's source and target launcher pods share one, because the VM's
	// VPC address is pinned to the VM and not to the pod.
	Targets []vpcTarget
	// Mixed is true when some backends are in a VPC and some are not.
	Mixed bool
	// Waiting names VPC backends cozyplane has not yet given an address.
	Waiting []string
	// Foreign names a VPC owned by another namespace, as "<ns>/<vpc>".
	Foreign string
}

// resolveVPCBackends reads the endpoint's backend pods. Not-ready endpoints
// count, so that a booting VM is recognised before its first readiness probe;
// terminating ones do not.
func (r *Reconciler) resolveVPCBackends(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment, svc *corev1.Service) (vpcBackends, error) {
	var out vpcBackends
	slices := &discoveryv1.EndpointSliceList{}
	if err := r.List(ctx, slices, client.InNamespace(svc.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name}); err != nil {
		return out, err
	}
	seenPods := map[string]bool{}
	seenTargets := map[vpcTarget]bool{}
	outside := 0
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			if e.TargetRef == nil || e.TargetRef.Kind != "Pod" || seenPods[e.TargetRef.Name] {
				continue
			}
			if e.Conditions.Terminating != nil && *e.Conditions.Terminating {
				continue
			}
			seenPods[e.TargetRef.Name] = true
			pod := &corev1.Pod{}
			if err := r.Get(ctx, types.NamespacedName{Namespace: svc.Namespace, Name: e.TargetRef.Name}, pod); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return out, err
			}
			out.Pods++
			ref, ok := pod.Annotations[vpcAnnotation]
			if !ok {
				outside++
				continue
			}
			out.InVPC = true
			vpc := ref
			if ns, name, cut := strings.Cut(ref, "/"); cut {
				if ns != ea.Namespace {
					out.Foreign = ref
				}
				vpc = name
			}
			ip := pod.Annotations[vpcIPAnnotation]
			if ip == "" {
				out.Waiting = append(out.Waiting, pod.Name)
				continue
			}
			t := vpcTarget{VPC: vpc, IP: ip}
			if !seenTargets[t] {
				seenTargets[t] = true
				out.Targets = append(out.Targets, t)
			}
		}
	}
	out.Mixed = out.InVPC && outside > 0
	sort.Slice(out.Targets, func(i, j int) bool { return out.Targets[i].IP < out.Targets[j].IP })
	sort.Strings(out.Waiting)
	return out, nil
}

// floatingIPServed reports whether the cluster serves cozyplane's FloatingIP.
func (r *Reconciler) floatingIPServed() (bool, error) {
	if _, err := r.RESTMapper().RESTMapping(floatingIPGVK.GroupKind(), floatingIPGVK.Version); err != nil {
		if isNoKindMatch(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ownedFloatingIP finds the FloatingIP this attachment controls, by
// controller owner reference. It returns nil when the kind is not served.
func (r *Reconciler) ownedFloatingIP(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment) (*unstructured.Unstructured, bool, error) {
	served, err := r.floatingIPServed()
	if err != nil || !served {
		return nil, served, err
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(floatingIPGVK.GroupVersion().WithKind("FloatingIPList"))
	if err := r.List(ctx, list, client.InNamespace(ea.Namespace)); err != nil {
		if isNoKindMatch(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	for i := range list.Items {
		if isControlledBy(&list.Items[i], ea) {
			return &list.Items[i], true, nil
		}
	}
	return nil, true, nil
}

func (r *Reconciler) withdrawFloatingIP(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment, fip *unstructured.Unstructured) error {
	if fip == nil {
		return nil
	}
	if err := r.Delete(ctx, fip); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("withdraw FloatingIP %s: %w", fip.GetName(), err)
	}
	r.event(ea, corev1.EventTypeNormal, "Withdrawn", "withdrew FloatingIP %s", fip.GetName())
	return nil
}

// bindsEndpoint reports whether a FloatingIP was bound for this endpoint. One
// without the record predates it and is taken to be.
func bindsEndpoint(fip *unstructured.Unstructured, endpoint string) bool {
	got, ok := fip.GetAnnotations()[endpointAnnotation]
	return !ok || got == endpoint
}

// floatingIPGenerateName leaves one character more than other children do:
// cozyplane names the Service it owns after the FloatingIP plus "-", and the
// apiserver validates that prefix as a DNS-1035 label too.
func floatingIPGenerateName(name string) string {
	prefix := childGenerateName(name)
	if len(prefix) > maxGenerateNamePrefix-1 {
		prefix = prefix[:maxGenerateNamePrefix-2] + "-"
	}
	return prefix
}

func floatingIPSpec(t vpcTarget, claim, lbClass string) map[string]interface{} {
	spec := map[string]interface{}{
		"vpcRef":           map[string]interface{}{"name": t.VPC},
		"target":           t.IP,
		"addressClaimName": claim,
	}
	if lbClass != "" {
		spec["loadBalancerClass"] = lbClass
	}
	return spec
}

// reconcileVPCAttachment publishes a whole-IP attachment whose backend sits in
// a cozyplane VPC through a FloatingIP instead of a cozy-proxy Service.
func (r *Reconciler) reconcileVPCAttachment(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment, own *corev1.Service,
	fip *unstructured.Unstructured, served bool, vb vpcBackends, view claimView, lbClass, endpoint string) (ctrl.Result, error) {
	lb := ea.Spec.LoadBalancer
	// The claim has one consumer. The FloatingIP's own Service is that
	// consumer here, so a Service this attachment rendered earlier goes first.
	if err := r.withdraw(ctx, ea, own); err != nil {
		return ctrl.Result{}, err
	}
	if !served {
		setUnprovisioned(ea, cozyv1alpha1.ReasonDatapathUnavailable,
			"the backend asks for a cozyplane VPC attachment, but this cluster does not serve sdn.cozystack.io FloatingIP; a cozy-proxy Service could not reach the VM's VPC address, so none is rendered")
		return ctrl.Result{RequeueAfter: requeueBackstop}, nil
	}
	if lb.Method != cozyv1alpha1.ExposureMethodWholeIP {
		setUnprovisioned(ea, cozyv1alpha1.ReasonMethodUnsupportedInVPC,
			fmt.Sprintf("method %s filters ports through cozy-proxy, which cannot reach a backend inside a cozyplane VPC, and a FloatingIP binds every port; use method WholeIP, or omit method and list ports", lb.Method))
		return ctrl.Result{}, r.withdrawFloatingIP(ctx, ea, fip)
	}
	if vb.Foreign != "" {
		setUnprovisioned(ea, cozyv1alpha1.ReasonVPCNotInNamespace,
			fmt.Sprintf("the backend is attached to VPC %s; a FloatingIP binds a VPC in its own namespace only, and must sit beside claim %s in %s", vb.Foreign, view.Name, ea.Namespace))
		return ctrl.Result{}, r.withdrawFloatingIP(ctx, ea, fip)
	}
	if len(vb.Targets) > 1 || vb.Mixed {
		what := describeTargets(vb.Targets)
		if vb.Mixed {
			what = "backends both inside and outside a VPC"
		}
		setUnprovisioned(ea, cozyv1alpha1.ReasonMultipleBackends,
			fmt.Sprintf("the endpoint resolves to more than one binding target (%s); a FloatingIP binds one, so the address stays held by claim %s and nothing is bound", what, view.Name))
		return ctrl.Result{}, r.withdrawFloatingIP(ctx, ea, fip)
	}
	if len(vb.Targets) == 0 {
		// A stopped VM, or a new launcher pod cozyplane has not stamped yet:
		// the VM keeps its VPC address, so an existing binding stays.
		if fip == nil {
			setUnprovisioned(ea, cozyv1alpha1.ReasonWaitingForVPCAddress,
				fmt.Sprintf("waiting for cozyplane to assign a VPC address to %s", strings.Join(vb.Waiting, ",")))
			return ctrl.Result{RequeueAfter: requeueBackstop}, nil
		}
		return r.reportFloatingIP(ea, fip, view), nil
	}

	desired := floatingIPSpec(vb.Targets[0], view.Name, lbClass)
	if fip == nil {
		u := newFloatingIP()
		u.SetNamespace(ea.Namespace)
		u.SetGenerateName(floatingIPGenerateName(ea.Name))
		u.SetLabels(map[string]string{OwnerLabel: ownerLabelValue(ea.Name)})
		u.SetAnnotations(map[string]string{endpointAnnotation: endpoint})
		u.SetOwnerReferences([]metav1.OwnerReference{controllerOwnerReference(ea)})
		u.Object["spec"] = desired
		if err := r.Create(ctx, u); err != nil {
			setUnprovisioned(ea, cozyv1alpha1.ReasonRenderFailed, fmt.Sprintf("creating the FloatingIP failed: %v", err))
			return ctrl.Result{}, fmt.Errorf("create FloatingIP: %w", err)
		}
		r.event(ea, corev1.EventTypeNormal, "Rendered", "created FloatingIP %s binding %s in VPC %s", u.GetName(), vb.Targets[0].IP, vb.Targets[0].VPC)
		fip = u
	} else if current, _, _ := unstructured.NestedMap(fip.Object, "spec"); !floatingIPSpecMatches(current, desired) || fip.GetAnnotations()[endpointAnnotation] != endpoint {
		fip.Object["spec"] = desired
		ann := fip.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[endpointAnnotation] = endpoint
		fip.SetAnnotations(ann)
		if err := r.Update(ctx, fip); err != nil {
			setUnprovisioned(ea, cozyv1alpha1.ReasonRenderFailed, fmt.Sprintf("updating FloatingIP %s failed: %v", fip.GetName(), err))
			return ctrl.Result{}, fmt.Errorf("update FloatingIP %s: %w", fip.GetName(), err)
		}
	}
	return r.reportFloatingIP(ea, fip, view), nil
}

// floatingIPSpecMatches compares the fields this controller writes, so fields
// cozyplane defaults are not fought over.
func floatingIPSpecMatches(current, desired map[string]interface{}) bool {
	for _, k := range []string{"target", "addressClaimName", "loadBalancerClass"} {
		cv, _, _ := unstructured.NestedString(current, k)
		dv, _, _ := unstructured.NestedString(desired, k)
		if cv != dv {
			return false
		}
	}
	cv, _, _ := unstructured.NestedString(current, "vpcRef", "name")
	dv, _, _ := unstructured.NestedString(desired, "vpcRef", "name")
	return cv == dv
}

// reportFloatingIP mirrors the FloatingIP's readiness into Provisioned. The
// attachment is provisioned once the FloatingIP is Ready wearing the claim's
// address.
func (r *Reconciler) reportFloatingIP(ea *cozyv1alpha1.EndpointAttachment, fip *unstructured.Unstructured, view claimView) ctrl.Result {
	ea.Status.ServiceName = ""
	phase, _, _ := unstructured.NestedString(fip.Object, "status", "phase")
	address, _, _ := unstructured.NestedString(fip.Object, "status", "address")
	vpc, _, _ := unstructured.NestedString(fip.Object, "spec", "vpcRef", "name")
	target, _, _ := unstructured.NestedString(fip.Object, "spec", "target")
	if !view.Bound {
		setUnprovisioned(ea, cozyv1alpha1.ReasonClaimPending,
			fmt.Sprintf("IPAddressClaim %s is not bound (%s); FloatingIP %s waits for it", view.Name, view.WaitingReason, fip.GetName()))
		return ctrl.Result{RequeueAfter: requeueBackstop}
	}
	if phase == floatingIPPhaseReady && address != "" && len(ea.Status.Addresses) > 0 &&
		len(missingAddresses(ea.Status.Addresses, []string{address})) == 0 {
		setProvisioned(ea, fmt.Sprintf("FloatingIP %s binds %s to %s in VPC %s", fip.GetName(), address, target, vpc))
		return r.settled(r.claimWatched && r.floatingIPWatched)
	}
	why := phase
	if why == "" {
		why = "Pending"
	}
	if c := firstFalseCondition(fip); c != "" {
		why += ": " + c
	}
	if address != "" && len(missingAddresses(ea.Status.Addresses, []string{address})) > 0 {
		why += fmt.Sprintf("; it wears %s, which does not cover %s", address, strings.Join(missingAddresses(ea.Status.Addresses, []string{address}), ","))
	}
	setUnprovisioned(ea, cozyv1alpha1.ReasonFloatingIPNotReady,
		fmt.Sprintf("FloatingIP %s binding %s in VPC %s is %s", fip.GetName(), target, vpc, why))
	return ctrl.Result{RequeueAfter: requeueBackstop}
}

func firstFalseCondition(u *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if status, _ := m["status"].(string); status == string(metav1.ConditionTrue) {
			continue
		}
		condType, _ := m["type"].(string)
		reason, _ := m["reason"].(string)
		msg, _ := m["message"].(string)
		out := condType
		if reason != "" {
			out += " " + reason
		}
		if msg != "" {
			out += " (" + msg + ")"
		}
		return out
	}
	return ""
}

func describeTargets(ts []vpcTarget) string {
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		parts = append(parts, t.VPC+"/"+t.IP)
	}
	return strings.Join(parts, ",")
}
