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

// Package endpointattachment reconciles EndpointAttachment objects: it
// resolves one tenant-facing Service of a managed application, draws an
// address through the address substrate, and renders one additive
// LoadBalancer Service that mirrors the endpoint. It is engine-agnostic and
// knows nothing about TLS, hostnames or the CNI in use.
package endpointattachment

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const (
	defaultApplicationGroup = "apps.cozystack.io"

	indexEndpointService = ".spec.endpoint.serviceName"
	indexClaimName       = ".spec.loadBalancer.claimName"
	indexApplication     = ".spec.applicationRef"

	// requeueBackstop covers the states that wait on the address substrate
	// when its kinds cannot be watched (CRDs absent at startup).
	requeueBackstop = time.Minute
)

// Reconciler reconciles EndpointAttachment objects.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// CozyProxyContractImplemented declares that a proxy answering to the
	// cozy-proxy service-proxy-name is deployed on this cluster. It is a
	// platform declaration, not a detection: without it a method-bearing
	// attachment renders no Service at all, because every proxy skips a
	// Service delegated to a name it does not claim while the load balancer
	// still attracts the address, leaving a routable address that drops
	// everything.
	CozyProxyContractImplemented bool

	// AllocateLoadBalancerNodePorts follows the platform's LoadBalancer
	// convention; false renders allocateLoadBalancerNodePorts: false.
	AllocateLoadBalancerNodePorts bool
}

// Reconcile drives one attachment through resolve, claim, render and report.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ea := &cozyv1alpha1.EndpointAttachment{}
	if err := r.Get(ctx, req.NamespacedName, ea); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ea.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	origStatus := ea.Status.DeepCopy()
	ea.Status.ObservedGeneration = ea.Generation

	res, rerr := r.reconcile(ctx, ea)
	projectPhase(ea)

	if !equality.Semantic.DeepEqual(origStatus, &ea.Status) {
		base := ea.DeepCopy()
		base.Status = *origStatus
		if err := r.Status().Patch(ctx, ea, client.MergeFrom(base)); err != nil {
			if rerr == nil {
				rerr = fmt.Errorf("update status: %w", err)
			}
		}
	}
	return res, rerr
}

func (r *Reconciler) reconcile(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment) (ctrl.Result, error) {
	lb := ea.Spec.LoadBalancer
	if lb == nil {
		setUnresolved(ea, cozyv1alpha1.ReasonRenderFailed, "no mechanism member is set")
		return ctrl.Result{}, nil
	}

	hr, err := r.applicationRelease(ctx, ea)
	if err != nil {
		return ctrl.Result{}, err
	}
	if hr == nil {
		setUnresolved(ea, cozyv1alpha1.ReasonApplicationNotFound,
			fmt.Sprintf("no %s %s in namespace %s", ea.Spec.ApplicationRef.Kind, ea.Spec.ApplicationRef.Name, ea.Namespace))
		return r.detach(ctx, ea, cozyv1alpha1.ReasonApplicationNotFound)
	}
	if ref := helmReleaseOwner(ea); ref != nil && ref.UID != hr.UID {
		setUnresolved(ea, cozyv1alpha1.ReasonApplicationIncarnationChange,
			fmt.Sprintf("%s %s was recreated (HelmRelease %s now has UID %s, this attachment belongs to UID %s); it will be garbage-collected with the previous incarnation",
				ea.Spec.ApplicationRef.Kind, ea.Spec.ApplicationRef.Name, hr.Name, hr.UID, ref.UID))
		return r.detach(ctx, ea, cozyv1alpha1.ReasonApplicationIncarnationChange)
	}
	if err := r.ensureAttachmentOwnership(ctx, ea, hr); err != nil {
		return ctrl.Result{}, err
	}

	endpoint := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ea.Namespace, Name: ea.Spec.Endpoint.ServiceName}, endpoint); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		setUnresolved(ea, cozyv1alpha1.ReasonEndpointNotFound,
			fmt.Sprintf("Service %s not found in namespace %s", ea.Spec.Endpoint.ServiceName, ea.Namespace))
		return r.detach(ctx, ea, cozyv1alpha1.ReasonEndpointNotFound)
	}
	if reason, msg := endpointDenied(ea, endpoint); reason != "" {
		setUnresolved(ea, reason, msg)
		return r.detach(ctx, ea, reason)
	}
	setResolved(ea, cozyv1alpha1.ReasonResolved,
		fmt.Sprintf("Service %s is a tenant-facing endpoint of %s %s", endpoint.Name, ea.Spec.ApplicationRef.Kind, ea.Spec.ApplicationRef.Name))

	claim, err := r.resolveClaim(ctx, ea)
	if err != nil {
		if !substrateInstalled(err) {
			setUnprovisioned(ea, cozyv1alpha1.ReasonClaimPending,
				"the address substrate (local.sdn.cozystack.io IPAddressClaim) is not installed on this cluster")
			return ctrl.Result{RequeueAfter: requeueBackstop}, nil
		}
		return ctrl.Result{}, err
	}
	if claim == nil {
		setUnprovisioned(ea, cozyv1alpha1.ReasonClaimNotFound,
			fmt.Sprintf("IPAddressClaim %s not found in namespace %s", lb.ClaimName, ea.Namespace))
		return ctrl.Result{RequeueAfter: requeueBackstop}, nil
	}
	view := parseClaim(claim)
	ea.Status.ClaimName = view.Name
	ea.Status.Addresses = nil
	for _, a := range view.Addresses {
		ea.Status.Addresses = append(ea.Status.Addresses, a.Address)
	}

	own, err := r.ownedService(ctx, ea)
	if err != nil {
		return ctrl.Result{}, err
	}

	if lb.ClaimName != "" && lb.Family != "" && view.Family != "" && view.Family != string(lb.Family) {
		setUnprovisioned(ea, cozyv1alpha1.ReasonFamilyConflict,
			fmt.Sprintf("attachment requests family %s but IPAddressClaim %s is %s", lb.Family, view.Name, view.Family))
		return ctrl.Result{}, r.withdraw(ctx, ea, own)
	}
	if view.ClassName == "" {
		setUnprovisioned(ea, cozyv1alpha1.ReasonClaimPending,
			fmt.Sprintf("IPAddressClaim %s has not resolved its class yet (%s)", view.Name, view.WaitingReason))
		return ctrl.Result{RequeueAfter: requeueBackstop}, nil
	}
	lbClass, err := r.classLoadBalancerClass(ctx, view.ClassName)
	if err != nil {
		if !substrateInstalled(err) {
			setUnprovisioned(ea, cozyv1alpha1.ReasonClaimPending,
				fmt.Sprintf("IPAddressClass %s of claim %s not found", view.ClassName, view.Name))
			return ctrl.Result{RequeueAfter: requeueBackstop}, nil
		}
		return ctrl.Result{}, err
	}
	if holder, err := r.foreignHolder(ctx, view, own); err != nil {
		return ctrl.Result{}, err
	} else if holder != nil {
		setUnprovisioned(ea, cozyv1alpha1.ReasonClaimInUse,
			fmt.Sprintf("IPAddressClaim %s is already associated to Service %s; one claim serves one Service", view.Name, holder))
		return ctrl.Result{RequeueAfter: requeueBackstop}, nil
	}

	ports := mirroredPorts(endpoint, lb)
	if len(ports) == 0 {
		setUnprovisioned(ea, cozyv1alpha1.ReasonNothingToPublish,
			fmt.Sprintf("Service %s has nothing to mirror (only the sentinel port %d or none of the requested ports); set ports or method", endpoint.Name, SentinelPort))
		return ctrl.Result{}, r.withdraw(ctx, ea, own)
	}

	dark := false
	if lb.Method != "" {
		if !r.CozyProxyContractImplemented {
			setUnprovisioned(ea, cozyv1alpha1.ReasonDatapathUnavailable,
				fmt.Sprintf("method %s needs a datapath implementing the %s Service contract, which this platform does not declare; no Service is rendered so the address is not attracted", lb.Method, ServiceProxyName))
			return ctrl.Result{}, r.withdraw(ctx, ea, own)
		}
		backends, err := r.backendPods(ctx, endpoint)
		if err != nil {
			return ctrl.Result{}, err
		}
		rival, err := r.delegatedRival(ctx, ea, own, backends)
		if err != nil {
			return ctrl.Result{}, err
		}
		if rival != nil {
			setUnprovisioned(ea, cozyv1alpha1.ReasonBackendAlreadyDelegated,
				fmt.Sprintf("backend %s of Service %s is already delegated to the datapath proxy by Service %s; the datapath maps one address per backend, so no Service is rendered", podNames(backends), endpoint.Name, rival.Name))
			return ctrl.Result{}, r.withdraw(ctx, ea, own)
		}
		if lb.Method == cozyv1alpha1.ExposureMethodWholeIP && len(backends) > 1 {
			dark = true
		}
	}

	desired := desiredService(ea, renderInput{
		Endpoint:          endpoint,
		ClaimName:         view.Name,
		LoadBalancerClass: lbClass,
		Dark:              dark,
		AllocateNodePorts: r.AllocateLoadBalancerNodePorts,
	}, ports)
	if own == nil {
		if err := r.Create(ctx, desired); err != nil {
			return ctrl.Result{}, fmt.Errorf("create Service: %w", err)
		}
		own = desired
		r.event(ea, corev1.EventTypeNormal, "Rendered", "rendered Service %s for endpoint %s", own.Name, endpoint.Name)
	} else {
		if !equalStringPtr(own.Spec.LoadBalancerClass, desired.Spec.LoadBalancerClass) {
			setUnprovisioned(ea, cozyv1alpha1.ReasonRenderFailed,
				fmt.Sprintf("Service %s carries loadBalancerClass %q but IPAddressClass %s fixes %q; the field is immutable, recreate the attachment", own.Name, derefString(own.Spec.LoadBalancerClass), view.ClassName, lbClass))
			ea.Status.ServiceName = own.Name
			return ctrl.Result{}, nil
		}
		if applyDesired(own, desired) {
			if err := r.Update(ctx, own); err != nil {
				return ctrl.Result{}, fmt.Errorf("update Service %s: %w", own.Name, err)
			}
		}
	}
	ea.Status.ServiceName = own.Name

	if dark {
		setUnprovisioned(ea, cozyv1alpha1.ReasonMultipleBackends,
			fmt.Sprintf("Service %s resolves to more than one ready backend; whole-IP NAT maps one address to one backend, so the address stays held by claim %s but nothing is served until exactly one backend remains", endpoint.Name, view.Name))
		return ctrl.Result{}, nil
	}
	if !view.Bound {
		setUnprovisioned(ea, cozyv1alpha1.ReasonClaimPending,
			fmt.Sprintf("IPAddressClaim %s is not bound (%s)", view.Name, view.WaitingReason))
		return ctrl.Result{RequeueAfter: requeueBackstop}, nil
	}
	assigned := ingressAddresses(own)
	if !intersects(assigned, ea.Status.Addresses) {
		setUnprovisioned(ea, cozyv1alpha1.ReasonWaitingForAddress,
			fmt.Sprintf("IPAddressClaim %s holds %s; waiting for Service %s to be assigned it", view.Name, strings.Join(ea.Status.Addresses, ","), own.Name))
		return ctrl.Result{RequeueAfter: requeueBackstop}, nil
	}
	setProvisioned(ea, fmt.Sprintf("Service %s carries %s from IPAddressClaim %s", own.Name, strings.Join(assigned, ","), view.Name))
	return ctrl.Result{}, nil
}

// applicationRelease finds the application's HelmRelease by the lineage
// labels the platform stamps on it.
func (r *Reconciler) applicationRelease(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment) (*helmv2.HelmRelease, error) {
	list := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, list, client.InNamespace(ea.Namespace), client.MatchingLabels(lineageLabels(ea.Spec.ApplicationRef))); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].CreationTimestamp.Before(&list.Items[j].CreationTimestamp)
	})
	return &list.Items[0], nil
}

func lineageLabels(ref cozyv1alpha1.EndpointApplicationReference) map[string]string {
	group := ref.Group
	if group == "" {
		group = defaultApplicationGroup
	}
	return map[string]string{
		appsv1alpha1.ApplicationGroupLabel: group,
		appsv1alpha1.ApplicationKindLabel:  ref.Kind,
		appsv1alpha1.ApplicationNameLabel:  ref.Name,
	}
}

func helmReleaseOwner(ea *cozyv1alpha1.EndpointAttachment) *metav1.OwnerReference {
	for i := range ea.OwnerReferences {
		ref := &ea.OwnerReferences[i]
		if ref.Kind == "HelmRelease" && strings.HasPrefix(ref.APIVersion, helmv2.GroupVersion.Group+"/") {
			return ref
		}
	}
	return nil
}

// ensureAttachmentOwnership binds the attachment to this incarnation of the
// application (an owner reference to its HelmRelease, matched by UID) and
// stamps the lineage labels so tooling lists it with the application's other
// objects. The reference is set once and never rewritten.
func (r *Reconciler) ensureAttachmentOwnership(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment, hr *helmv2.HelmRelease) error {
	base := ea.DeepCopy()
	changed := false
	if helmReleaseOwner(ea) == nil {
		ea.OwnerReferences = append(ea.OwnerReferences, metav1.OwnerReference{
			APIVersion: helmv2.GroupVersion.String(),
			Kind:       "HelmRelease",
			Name:       hr.Name,
			UID:        hr.UID,
		})
		changed = true
	}
	if ea.Labels == nil {
		ea.Labels = map[string]string{}
	}
	for k, v := range lineageLabels(ea.Spec.ApplicationRef) {
		if ea.Labels[k] != v {
			ea.Labels[k] = v
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := r.Patch(ctx, ea, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("bind attachment to HelmRelease %s: %w", hr.Name, err)
	}
	return nil
}

// endpointDenied applies the authorization seam: the Service must carry the
// lineage identity labels matching applicationRef on all three fields AND
// the tenant-facing marker. Lineage alone also covers operator-internal and
// headless Services; the marker is what the platform stamps on exactly the
// Services ApplicationDefinition.spec.services selects.
func endpointDenied(ea *cozyv1alpha1.EndpointAttachment, svc *corev1.Service) (reason, message string) {
	if ref := metav1.GetControllerOf(svc); ref != nil && ref.Kind == attachmentGVK.Kind && strings.HasPrefix(ref.APIVersion, attachmentGVK.Group+"/") {
		return cozyv1alpha1.ReasonEndpointNotTenantFacing,
			fmt.Sprintf("Service %s is rendered by an EndpointAttachment and is not an application endpoint", svc.Name)
	}
	for k, want := range lineageLabels(ea.Spec.ApplicationRef) {
		if got := svc.Labels[k]; got != want {
			return cozyv1alpha1.ReasonEndpointLineageMismatch,
				fmt.Sprintf("Service %s has %s=%q, attachment targets %q", svc.Name, k, got, want)
		}
	}
	if svc.Labels[corev1alpha1.TenantResourceLabelKey] != corev1alpha1.TenantResourceLabelValue {
		return cozyv1alpha1.ReasonEndpointNotTenantFacing,
			fmt.Sprintf("Service %s is not marked tenant-facing (%s) by the platform; only Services the application definition declares as endpoints can be attached", svc.Name, corev1alpha1.TenantResourceLabelKey)
	}
	return "", ""
}

// resolveClaim returns the claim the attachment consumes: the referenced
// one, or the one it minted (minting it on first sight). A nil claim with a
// nil error means the referenced claim does not exist.
func (r *Reconciler) resolveClaim(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment) (*unstructured.Unstructured, error) {
	if name := ea.Spec.LoadBalancer.ClaimName; name != "" {
		u := newClaim()
		if err := r.Get(ctx, types.NamespacedName{Namespace: ea.Namespace, Name: name}, u); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		return u, nil
	}
	u, err := r.mintedClaim(ctx, ea)
	if err != nil || u != nil {
		return u, err
	}
	u, err = r.mintClaim(ctx, ea)
	if err != nil {
		return nil, err
	}
	r.event(ea, corev1.EventTypeNormal, "ClaimMinted", "minted IPAddressClaim %s", u.GetName())
	return u, nil
}

// foreignHolder reports a live Service, other than this attachment's own,
// that the claim's addresses are associated to. Such a claim is not fought
// over.
func (r *Reconciler) foreignHolder(ctx context.Context, view claimView, own *corev1.Service) (*types.NamespacedName, error) {
	for _, a := range view.Addresses {
		if a.Name == "" {
			continue
		}
		holder, err := r.addressHolder(ctx, a.Name)
		if err != nil {
			if !substrateInstalled(err) {
				return nil, nil
			}
			return nil, err
		}
		if holder == nil {
			continue
		}
		if own != nil && holder.Namespace == own.Namespace && holder.Name == own.Name {
			continue
		}
		live := &corev1.Service{}
		if err := r.Get(ctx, *holder, live); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		return holder, nil
	}
	return nil, nil
}

// detach is the endpoint-absent path: the rendered Service is torn down,
// the claim (minted or referenced) is kept so the address survives, and the
// Provisioned reason records that an address is held with nothing published.
func (r *Reconciler) detach(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment, reason string) (ctrl.Result, error) {
	own, err := r.ownedService(ctx, ea)
	if err != nil {
		return ctrl.Result{}, err
	}
	if own != nil || ea.Status.ClaimName != "" {
		setUnprovisioned(ea, cozyv1alpha1.ReasonEndpointDetached,
			fmt.Sprintf("endpoint is absent; the rendered Service was withdrawn and the address stays held by IPAddressClaim %s", ea.Status.ClaimName))
	} else {
		setUnprovisioned(ea, reason, "nothing rendered: the endpoint did not resolve")
	}
	return ctrl.Result{}, r.withdraw(ctx, ea, own)
}

// withdraw deletes the rendered Service, if any, and clears status.serviceName.
func (r *Reconciler) withdraw(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment, own *corev1.Service) error {
	ea.Status.ServiceName = ""
	if own == nil {
		return nil
	}
	if err := r.Delete(ctx, own); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("withdraw Service %s: %w", own.Name, err)
	}
	r.event(ea, corev1.EventTypeNormal, "Withdrawn", "withdrew Service %s", own.Name)
	return nil
}

func (r *Reconciler) event(ea *cozyv1alpha1.EndpointAttachment, eventType, reason, format string, args ...interface{}) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(ea, eventType, reason, format, args...)
}

func podNames(pods map[types.UID]string) string {
	names := make([]string, 0, len(pods))
	for _, n := range pods {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func applicationKey(ref cozyv1alpha1.EndpointApplicationReference) string {
	l := lineageLabels(ref)
	return l[appsv1alpha1.ApplicationGroupLabel] + "/" + l[appsv1alpha1.ApplicationKindLabel] + "/" + l[appsv1alpha1.ApplicationNameLabel]
}

// SetupWithManager registers the controller, its field indices and the
// watches that make mirroring event-driven. The address-substrate kinds are
// watched when they are served at startup; otherwise the requeue backstop
// covers them.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	indexer := mgr.GetFieldIndexer()
	if err := indexer.IndexField(ctx, &cozyv1alpha1.EndpointAttachment{}, indexEndpointService, func(o client.Object) []string {
		return []string{o.(*cozyv1alpha1.EndpointAttachment).Spec.Endpoint.ServiceName}
	}); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &cozyv1alpha1.EndpointAttachment{}, indexClaimName, func(o client.Object) []string {
		ea := o.(*cozyv1alpha1.EndpointAttachment)
		if ea.Spec.LoadBalancer == nil || ea.Spec.LoadBalancer.ClaimName == "" {
			return nil
		}
		return []string{ea.Spec.LoadBalancer.ClaimName}
	}); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &cozyv1alpha1.EndpointAttachment{}, indexApplication, func(o client.Object) []string {
		return []string{applicationKey(o.(*cozyv1alpha1.EndpointAttachment).Spec.ApplicationRef)}
	}); err != nil {
		return err
	}

	b := ctrl.NewControllerManagedBy(mgr).
		Named("endpointattachment-controller").
		For(&cozyv1alpha1.EndpointAttachment{}).
		Owns(&corev1.Service{}).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.mapService)).
		Watches(&discoveryv1.EndpointSlice{}, handler.EnqueueRequestsFromMapFunc(r.mapEndpointSlice)).
		Watches(&helmv2.HelmRelease{}, handler.EnqueueRequestsFromMapFunc(r.mapHelmRelease))

	if _, err := mgr.GetRESTMapper().RESTMapping(claimGVK.GroupKind(), claimGVK.Version); err == nil {
		b = b.Owns(newClaim()).
			Watches(newClaim(), handler.EnqueueRequestsFromMapFunc(r.mapClaim))
	} else if !isNoKindMatch(err) {
		return err
	} else {
		mgr.GetLogger().Info("address substrate not served; claim changes are picked up by periodic requeue", "kind", claimGVK.String())
	}
	return b.Complete(r)
}

func (r *Reconciler) attachmentsByIndex(ctx context.Context, namespace, index, value string) []reconcile.Request {
	list := &cozyv1alpha1.EndpointAttachmentList{}
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingFields{index: value}); err != nil {
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for _, ea := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ea)})
	}
	return out
}

// methodAttachments enqueues every attachment in the namespace that renders
// a delegated Service, since any delegated Service or endpoint change there
// can move the exclusivity verdict.
func (r *Reconciler) methodAttachments(ctx context.Context, namespace string) []reconcile.Request {
	list := &cozyv1alpha1.EndpointAttachmentList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, ea := range list.Items {
		if ea.Spec.LoadBalancer != nil && ea.Spec.LoadBalancer.Method != "" {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ea)})
		}
	}
	return out
}

func (r *Reconciler) mapService(ctx context.Context, o client.Object) []reconcile.Request {
	svc, ok := o.(*corev1.Service)
	if !ok {
		return nil
	}
	reqs := r.attachmentsByIndex(ctx, svc.Namespace, indexEndpointService, svc.Name)
	if svc.Labels[ServiceProxyNameLabel] == ServiceProxyName {
		reqs = append(reqs, r.methodAttachments(ctx, svc.Namespace)...)
	}
	return reqs
}

func (r *Reconciler) mapEndpointSlice(ctx context.Context, o client.Object) []reconcile.Request {
	name := o.GetLabels()[discoveryv1.LabelServiceName]
	if name == "" {
		return nil
	}
	reqs := r.attachmentsByIndex(ctx, o.GetNamespace(), indexEndpointService, name)
	return append(reqs, r.methodAttachments(ctx, o.GetNamespace())...)
}

func (r *Reconciler) mapHelmRelease(ctx context.Context, o client.Object) []reconcile.Request {
	l := o.GetLabels()
	kind, name := l[appsv1alpha1.ApplicationKindLabel], l[appsv1alpha1.ApplicationNameLabel]
	if kind == "" || name == "" {
		return nil
	}
	key := applicationKey(cozyv1alpha1.EndpointApplicationReference{Group: l[appsv1alpha1.ApplicationGroupLabel], Kind: kind, Name: name})
	return r.attachmentsByIndex(ctx, o.GetNamespace(), indexApplication, key)
}

func (r *Reconciler) mapClaim(ctx context.Context, o client.Object) []reconcile.Request {
	return r.attachmentsByIndex(ctx, o.GetNamespace(), indexClaimName, o.GetName())
}
