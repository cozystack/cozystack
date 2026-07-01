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

// Package serviceexposure hosts the controller that reconciles
// network.cozystack.io/v1alpha1 ServiceExposure resources into the
// backend-specific LoadBalancer pool and announcer CRs (MetalLB
// IPAddressPool + L2Advertisement, Cilium CiliumLoadBalancerIPPool +
// CiliumL2AnnouncementPolicy, or nothing for externalIPs / robotlb),
// resolving the requested ExposureClass to a concrete backend.
//
// Charts render ServiceExposure CRs; this controller owns everything
// downstream. It deliberately never mutates the target Service — that
// object is owned by the chart that renders it, so mutating it would
// create a Helm-vs-controller drift loop. The backend pool/announcer CRs
// are scoped to the Service via the pool CR's own selector instead.
package serviceexposure

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
	"github.com/cozystack/cozystack/internal/controller/serviceexposure/backend"
)

// +kubebuilder:rbac:groups=network.cozystack.io,resources=serviceexposures;exposureclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.cozystack.io,resources=serviceexposures/status;exposureclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=network.cozystack.io,resources=serviceexposures/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=metallb.io,resources=ipaddresspools;l2advertisements,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumloadbalancerippools;ciliuml2announcementpolicies,verbs=get;list;watch;create;update;patch;delete

// Reconciler reconciles ServiceExposure resources, owning the downstream
// backend pool/announcer CRs.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile resolves the ServiceExposure's ExposureClass, reconciles the
// class-level backend pool/announcer (shared by every exposure of the
// class, scoped to their namespaces), then reports the assigned IPs for
// this exposure's Service in status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	exp := &networkv1alpha1.ServiceExposure{}
	if err := r.Get(ctx, req.NamespacedName, exp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	class, err := r.resolveClass(ctx, exp)

	// Deletion: recompute the class without this exposure (it is excluded
	// from the namespace set because it is being deleted), which updates or
	// garbage-collects the shared pool, then drop the finalizer.
	if !exp.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(exp, exposureFinalizer) {
			switch {
			case class != nil:
				if rcErr := r.reconcileClass(ctx, class); rcErr != nil {
					// A transient error requeues so the shared pool is still
					// recomputed before the exposure goes away. Only the two
					// deterministic, config-driven reasons fall through:
					// BackendRenderError (e.g. addresses cleared) and the
					// defensive BackendUnsupported (unreachable today — the
					// CRD enum is pinned to registered backends — but treated
					// the same so an enum/registry drift cannot wedge deletes).
					if reason := reasonOf(rcErr); reason != "BackendRenderError" && reason != "BackendUnsupported" {
						return ctrl.Result{}, rcErr
					}
					// Deterministic render error: the class can't produce a
					// valid pool (e.g. addresses cleared while exposures are
					// live). A pool created when the class WAS valid is now
					// frozen with a stale serviceAllocation/serviceSelector
					// that would keep this deleted namespace in scope — an
					// isolation leak. Reclaim the class's CRs entirely rather
					// than drop the finalizer over a stale pool; a surviving
					// exposure recreates a correctly-scoped pool once the
					// class is fixed. Then the finalizer can be removed.
					if gcErr := reclaimClassResources(ctx, r.Client, class.Name, map[objectKey]struct{}{}); gcErr != nil {
						return ctrl.Result{}, gcErr
					}
					log.FromContext(ctx).Info("reclaimed CRs of a misconfigured class during exposure deletion", "class", class.Name, "reason", reasonOf(rcErr))
				}
			case err != nil && reasonOf(err) != "ClassNotFound":
				// A transient resolveClass error (apiserver hiccup) — or a
				// config error like AmbiguousDefaultClass — must NOT drop
				// the finalizer: doing so would delete the exposure without
				// recomputing the shared pool, leaking it or leaving a
				// stale namespace in its selector. Requeue, and log so an
				// operator can see why a delete is not progressing.
				log.FromContext(ctx).Info("deferring ServiceExposure deletion until its class resolves", "reason", reasonOf(err), "error", err.Error())
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(exp, exposureFinalizer)
			if err := r.Update(ctx, exp); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(exp, exposureFinalizer) {
		if err := r.Update(ctx, exp); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	if err := r.runReconcileSteps(ctx, exp, class, err); err != nil {
		if statusErr := r.markFailed(ctx, exp, err); statusErr != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile failed: %w (status update also failed: %v)", err, statusErr)
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) runReconcileSteps(ctx context.Context, exp *networkv1alpha1.ServiceExposure, class *networkv1alpha1.ExposureClass, classErr error) error {
	if classErr != nil {
		return classErr
	}

	if err := r.reconcileClass(ctx, class); err != nil {
		return err
	}

	b, err := backend.For(class.Spec.Backend)
	if err != nil {
		return failf("BackendUnsupported", err)
	}

	svc := &corev1.Service{}
	svcName := types.NamespacedName{Namespace: exp.Namespace, Name: exp.Spec.ServiceRef.Name}
	if err := r.Get(ctx, svcName, svc); err != nil {
		if apierrors.IsNotFound(err) {
			return failf("ServiceNotFound", fmt.Errorf("service %s not found", svcName))
		}
		return fmt.Errorf("get service %s: %w", svcName, err)
	}

	assignedIPs, ready, notReady := b.Observe(svc)
	return r.markReady(ctx, exp, string(class.Spec.Backend), assignedIPs, ready, notReady)
}

// reconcileClass renders the class-level pool/announcer scoped to the
// namespaces of every live (non-deleting) ServiceExposure resolving to the
// class, and garbage-collects the class's CRs no longer wanted (including
// all of them when the last exposure goes away or the backend changes).
func (r *Reconciler) reconcileClass(ctx context.Context, class *networkv1alpha1.ExposureClass) error {
	// A terminating class is cleaned up by the ExposureClass controller;
	// don't re-render its pool here or the two would race.
	if !class.DeletionTimestamp.IsZero() {
		return nil
	}

	b, err := backend.For(class.Spec.Backend)
	if err != nil {
		return failf("BackendUnsupported", err)
	}

	namespaces, err := r.namespacesForClass(ctx, class)
	if err != nil {
		return err
	}

	desired, err := b.Desired(class, namespaces)
	if err != nil {
		return failf("BackendRenderError", err)
	}

	return r.applyDesired(ctx, class.Name, desired)
}

// namespacesForClass returns the sorted, unique namespaces of every
// non-deleting ServiceExposure that resolves to the class.
func (r *Reconciler) namespacesForClass(ctx context.Context, class *networkv1alpha1.ExposureClass) ([]string, error) {
	isDefault := class.Annotations[networkv1alpha1.IsDefaultExposureClassAnnotation] == "true"

	list := &networkv1alpha1.ServiceExposureList{}
	if err := r.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list ServiceExposures for class %q: %w", class.Name, err)
	}
	seen := map[string]struct{}{}
	for i := range list.Items {
		e := &list.Items[i]
		if !e.DeletionTimestamp.IsZero() {
			continue
		}
		if !exposureReferencesClass(e, class.Name, isDefault) {
			continue
		}
		seen[e.Namespace] = struct{}{}
	}
	namespaces := make([]string, 0, len(seen))
	for ns := range seen {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

// resolveClass returns the named ExposureClass, or the cluster default
// (annotated is-default-class=true) when the exposure leaves the name
// empty.
func (r *Reconciler) resolveClass(ctx context.Context, exp *networkv1alpha1.ServiceExposure) (*networkv1alpha1.ExposureClass, error) {
	if name := exp.Spec.ExposureClassName; name != "" {
		class := &networkv1alpha1.ExposureClass{}
		if err := r.Get(ctx, types.NamespacedName{Name: name}, class); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, failf("ClassNotFound", fmt.Errorf("exposureClass %q not found", name))
			}
			return nil, fmt.Errorf("get exposureClass %q: %w", name, err)
		}
		return class, nil
	}

	list := &networkv1alpha1.ExposureClassList{}
	if err := r.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list exposureClasses: %w", err)
	}
	var defaults []*networkv1alpha1.ExposureClass
	for i := range list.Items {
		if list.Items[i].Annotations[networkv1alpha1.IsDefaultExposureClassAnnotation] == "true" {
			defaults = append(defaults, &list.Items[i])
		}
	}
	switch len(defaults) {
	case 0:
		return nil, failf("ClassNotFound", fmt.Errorf("no exposureClassName set and no default ExposureClass found"))
	case 1:
		return defaults[0], nil
	default:
		return nil, failf("AmbiguousDefaultClass", fmt.Errorf("found %d default ExposureClasses, expected at most one", len(defaults)))
	}
}

// applyDesired creates/updates the class-level desired CRs and
// garbage-collects any previously-owned CRs of the class no longer in the
// desired set (a backend change, or the last exposure going away, yields
// an empty desired set and reclaims everything).
func (r *Reconciler) applyDesired(ctx context.Context, className string, desired []*unstructured.Unstructured) error {
	wanted := make(map[objectKey]struct{}, len(desired))
	for _, obj := range desired {
		stamp(obj, className)
		wanted[keyOf(obj)] = struct{}{}
		if err := r.applyObject(ctx, className, obj); err != nil {
			return err
		}
	}
	return r.garbageCollect(ctx, className, wanted)
}

// applyObject creates the CR or updates its spec, refusing to take over a
// same-named object this controller does not manage.
func (r *Reconciler) applyObject(ctx context.Context, className string, desired *unstructured.Unstructured) error {
	logger := log.FromContext(ctx)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	err := r.Get(ctx, types.NamespacedName{Namespace: desired.GetNamespace(), Name: desired.GetName()}, existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			if backendCRDMissing(err) {
				return failf("BackendUnavailable", fmt.Errorf("%s CRD not installed for the configured backend (resource %s): %w", desired.GetKind(), desired.GetName(), err))
			}
			return fmt.Errorf("create %s %s: %w", desired.GetKind(), desired.GetName(), err)
		}
		logger.V(1).Info("created backend resource", "kind", desired.GetKind(), "name", desired.GetName(), "namespace", desired.GetNamespace())
		return nil
	case backendCRDMissing(err):
		// On a live cluster a missing backend CRD surfaces here (the
		// RESTMapper cannot map the GVK), not on Create.
		return failf("BackendUnavailable", fmt.Errorf("%s CRD not installed for the configured backend (resource %s): %w", desired.GetKind(), desired.GetName(), err))
	case err != nil:
		return fmt.Errorf("get %s %s: %w", desired.GetKind(), desired.GetName(), err)
	}

	if !ownedByClass(existing, className) {
		return failf("ResourceConflict", fmt.Errorf("%s %s exists but is not managed by ExposureClass %q; refusing to take it over", desired.GetKind(), desired.GetName(), className))
	}

	// Reconcile only the keys this controller manages (backend.ManagedSpecKeys).
	// The apiserver adds CRD-defaulted keys (metallb IPAddressPool
	// autoAssign/avoidBuggyIPs, cilium pool disabled/allowFirstLastIPs) the
	// backend never sets; a whole-spec DeepEqual would always differ,
	// rewrite spec every reconcile, and transiently strip those defaults.
	// We add/update managed keys and DELETE managed keys absent from the
	// fresh render (so clearing class.spec.interfaces propagates), while
	// leaving server-defaulted keys intact.
	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	existingSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	if existingSpec == nil {
		existingSpec = map[string]interface{}{}
	}
	changed := false
	for _, k := range backend.ManagedSpecKeys(desired.GetKind()) {
		dv, want := desiredSpec[k]
		ev, have := existingSpec[k]
		switch {
		case want && (!have || !equality.Semantic.DeepEqual(ev, dv)):
			existingSpec[k] = dv
			changed = true
		case !want && have:
			delete(existingSpec, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}

	existing.Object["spec"] = existingSpec
	stamp(existing, className)
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("update %s %s: %w", desired.GetKind(), desired.GetName(), err)
	}
	logger.V(1).Info("updated backend resource", "kind", desired.GetKind(), "name", desired.GetName())
	return nil
}

// garbageCollect deletes CRs owned by the class that are no longer wanted.
func (r *Reconciler) garbageCollect(ctx context.Context, className string, wanted map[objectKey]struct{}) error {
	return reclaimClassResources(ctx, r.Client, className, wanted)
}

// reclaimClassResources deletes every managed CR owned by the class that is
// not in the wanted set. An empty wanted set reclaims them all (used when
// the last exposure of the class goes away, the backend changes, or the
// ExposureClass itself is deleted). Shared by the exposure and class
// reconcilers.
func reclaimClassResources(ctx context.Context, c client.Client, className string, wanted map[objectKey]struct{}) error {
	logger := log.FromContext(ctx)
	for _, gvk := range backend.ManagedGVKs() {
		list := managedListFor(gvk)
		if err := c.List(ctx, list, client.MatchingLabels{managedByLabel: managedByValue}); err != nil {
			if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
				continue // CRD for this backend not installed; nothing to GC
			}
			return fmt.Errorf("list %s for GC: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			obj := &list.Items[i]
			if !ownedByClass(obj, className) {
				continue
			}
			if _, keep := wanted[keyOf(obj)]; keep {
				continue
			}
			if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete orphan %s %s: %w", obj.GetKind(), obj.GetName(), err)
			}
			logger.V(1).Info("garbage-collected backend resource", "kind", obj.GetKind(), "name", obj.GetName())
		}
	}
	return nil
}

// SetupWithManager wires the controller. The target Service is watched
// (not Owned — the controller never sets an ownerRef on it) so a freshly
// assigned LoadBalancer IP refreshes status; ExposureClass edits re-sync
// every referencing exposure.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("serviceexposure-controller").
		For(&networkv1alpha1.ServiceExposure{}).
		Watches(&corev1.Service{}, r.serviceToExposure()).
		Watches(&networkv1alpha1.ExposureClass{}, r.classToExposures()).
		Complete(r)
}
