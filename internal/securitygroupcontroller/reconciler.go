// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

// Package securitygroupcontroller maintains SecurityGroup membership labels.
//
// A SecurityGroup (served by cozystack-api as a projection over a marked
// CiliumNetworkPolicy) owns a membership label securitygroup.sdn.cozystack.io/
// <name>. The backing policy's endpointSelector matches that label, so the
// policy applies to exactly the pods carrying it. This controller is the only
// writer of those labels: for each marked policy it stamps the membership label
// onto the pods of the applications listed in the policy's attachments
// annotation, and removes it when an application is detached or the
// SecurityGroup is deleted.
//
// The controller never trusts the attachment list to pick pods directly: it
// resolves each attachment to the lineage labels the lineage webhook stamps on
// managed-app pods (apps.cozystack.io/application.{group,kind,name}) and only
// ever labels pods that already carry those labels in the SecurityGroup's own
// namespace. That, plus single-key merge patches, keeps a tenant-driven
// cluster-wide pod-label writer from reaching pods a tenant could not otherwise
// address.
package securitygroupcontroller

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
)

const (
	// sgLabelKey marks the CiliumNetworkPolicy objects owned by the SecurityGroup
	// API. It mirrors the constant in pkg/registry/sdn/securitygroup.
	sgLabelKey   = "sdn.cozystack.io/securitygroup"
	sgLabelValue = "true"

	// attachmentsAnnotation holds the SecurityGroup's attachments as a JSON array
	// of ApplicationReference. It mirrors the constant the REST storage writes.
	attachmentsAnnotation = "sdn.cozystack.io/attachments"

	// membershipFinalizer guards the backing policy so the controller can strip
	// the membership labels off member pods before the policy disappears. Shared
	// with the REST storage, which re-asserts it on write.
	membershipFinalizer = sdnv1alpha1.MembershipFinalizer

	// appGroupLabelKey, appKindLabelKey and appNameLabelKey are the lineage
	// labels the lineage webhook stamps on managed-app pods. Mirrored here to
	// keep the controller from depending on internal/lineagecontrollerwebhook.
	appGroupLabelKey = "apps.cozystack.io/application.group"
	appKindLabelKey  = "apps.cozystack.io/application.kind"
	appNameLabelKey  = "apps.cozystack.io/application.name"

	defaultAppGroup = "apps.cozystack.io"

	// managedByLabel marks pods the lineage webhook manages — the only pods that
	// can ever be SecurityGroup members. The manager caches only these pods.
	managedByLabel = "internal.cozystack.io/managed-by-cozystack"

	// membershipResyncInterval bounds how long membership can stay diverged if a
	// pod event is ever missed or raced. A successful reconcile requeues itself
	// after this interval as a safety net on top of the prompt, event-driven pod
	// watch — far tighter than the manager's default SyncPeriod.
	membershipResyncInterval = 10 * time.Minute
)

// CacheByObject bounds the manager's informers: pods are cached only when
// managed by Cozystack (the only pods that can be members) and
// CiliumNetworkPolicies only when SecurityGroup-owned, keeping the controller's
// cache small in a busy cluster. A managed pod never loses the managed-by label,
// so scoping by it cannot hide a pod whose membership must later be removed.
func CacheByObject() map[client.Object]cache.ByObject {
	return map[client.Object]cache.ByObject{
		&corev1.Pod{}:          {Label: labels.SelectorFromSet(labels.Set{managedByLabel: "true"})},
		&CiliumNetworkPolicy{}: {Label: labels.SelectorFromSet(labels.Set{sgLabelKey: sgLabelValue})},
	}
}

// membershipLabelKey returns the membership label key for a SecurityGroup name.
func membershipLabelKey(name string) string {
	return sdnv1alpha1.MembershipLabelPrefix + name
}

// appLabels projects an ApplicationReference into the lineage labels that select
// the referenced application's pods. It mirrors the REST storage so the
// controller resolves attachments to exactly the pods the projection targets.
func appLabels(ref sdnv1alpha1.ApplicationReference) map[string]string {
	group := ref.APIGroup
	if group == "" {
		group = defaultAppGroup
	}
	return map[string]string{
		appGroupLabelKey: group,
		appKindLabelKey:  ref.Kind,
		appNameLabelKey:  ref.Name,
	}
}

// decodeAttachments parses the attachments annotation. A missing value yields
// nil; a malformed value is treated as empty (so a bad annotation cannot wedge
// the reconciler) but is logged, since silently swallowing it hides a real
// defect in whatever wrote the annotation.
func decodeAttachments(ctx context.Context, s string) []sdnv1alpha1.ApplicationReference {
	if s == "" {
		return nil
	}
	var refs []sdnv1alpha1.ApplicationReference
	if err := json.Unmarshal([]byte(s), &refs); err != nil {
		log.FromContext(ctx).Error(err, "ignoring malformed attachments annotation", "annotation", attachmentsAnnotation)
		return nil
	}
	return refs
}

// Reconciler keeps SecurityGroup membership labels in sync with attachments.
type Reconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;patch

// Reconcile brings a single SecurityGroup's membership labels to the desired
// state: the union of its attachments' pods carries the membership label, and
// nothing else does.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cnp := &CiliumNetworkPolicy{}
	if err := r.Get(ctx, req.NamespacedName, cnp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Only act on SecurityGroup-owned policies; a marker-less policy reaching the
	// queue (e.g. a label flip) is none of this controller's business.
	if cnp.Labels[sgLabelKey] != sgLabelValue {
		return ctrl.Result{}, nil
	}

	ns := cnp.Namespace
	key := membershipLabelKey(cnp.Name)

	// Deletion: strip the membership label off every member pod, then drop the
	// finalizer so the policy can be garbage-collected.
	if !cnp.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(cnp, membershipFinalizer) {
			if err := r.stripMembership(ctx, ns, key); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.removeFinalizer(ctx, cnp); err != nil {
				return ctrl.Result{}, err
			}
			logger.V(1).Info("released SecurityGroup membership", "securitygroup", cnp.Name, "namespace", ns)
		}
		return ctrl.Result{}, nil
	}

	if err := r.ensureFinalizer(ctx, cnp); err != nil {
		return ctrl.Result{}, err
	}

	// Desired members: pods matching any attachment's lineage labels in this
	// namespace. Resolving through lineage labels — not the attachment list
	// directly — is the boundary that stops a SecurityGroup from labeling pods a
	// tenant could not otherwise select.
	desired := map[string]struct{}{}
	for _, app := range decodeAttachments(ctx, cnp.Annotations[attachmentsAnnotation]) {
		pods := &corev1.PodList{}
		if err := r.List(ctx, pods, client.InNamespace(ns), client.MatchingLabels(appLabels(app))); err != nil {
			return ctrl.Result{}, err
		}
		for i := range pods.Items {
			if err := r.addMembership(ctx, &pods.Items[i], key); err != nil {
				return ctrl.Result{}, err
			}
			desired[pods.Items[i].Name] = struct{}{}
		}
	}

	// Current members: pods carrying the membership label. Any that are no longer
	// desired (the app was detached) get the label removed.
	current := &corev1.PodList{}
	if err := r.List(ctx, current, client.InNamespace(ns), client.MatchingLabels{key: ""}); err != nil {
		return ctrl.Result{}, err
	}
	for i := range current.Items {
		if _, ok := desired[current.Items[i].Name]; ok {
			continue
		}
		if err := r.removeMembership(ctx, &current.Items[i], key); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Re-queue a periodic resync as a safety net. The pod watch is the prompt
	// path — a new managed-app pod fires mapPodToSGs and re-reconciles within
	// milliseconds. But desired/current are computed from the cached lister, so
	// if a pod's membership ever fails to converge from an event (a missed or
	// raced informer delivery, an add/remove patch lost to a transient API
	// error swallowed by a later success, etc.) this bounded resync re-lists
	// from a now-current cache and repairs it without waiting for the manager's
	// hours-long default SyncPeriod. It is cheap (one reconcile per SecurityGroup
	// per interval) and keeps membership eventually consistent regardless of
	// event delivery.
	return ctrl.Result{RequeueAfter: membershipResyncInterval}, nil
}

// stripMembership removes the membership label from every pod that carries it in
// the namespace. Used on SecurityGroup deletion.
func (r *Reconciler) stripMembership(ctx context.Context, ns, key string) error {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(ns), client.MatchingLabels{key: ""}); err != nil {
		return err
	}
	for i := range pods.Items {
		if err := r.removeMembership(ctx, &pods.Items[i], key); err != nil {
			return err
		}
	}
	return nil
}

// addMembership stamps the membership label onto a pod with a single-key merge
// patch, so it never clobbers another SecurityGroup's label or the pod's lineage
// labels.
func (r *Reconciler) addMembership(ctx context.Context, pod *corev1.Pod, key string) error {
	if _, ok := pod.Labels[key]; ok {
		return nil
	}
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[key] = ""
	return r.Patch(ctx, pod, patch)
}

// removeMembership clears the membership label off a pod with a single-key merge
// patch.
func (r *Reconciler) removeMembership(ctx context.Context, pod *corev1.Pod, key string) error {
	if _, ok := pod.Labels[key]; !ok {
		return nil
	}
	patch := client.MergeFrom(pod.DeepCopy())
	delete(pod.Labels, key)
	return r.Patch(ctx, pod, patch)
}

// ensureFinalizer adds the membership finalizer to the backing policy. The
// finalizer is changed through a merge patch, never an Update, so the policy's
// spec is left untouched.
func (r *Reconciler) ensureFinalizer(ctx context.Context, cnp *CiliumNetworkPolicy) error {
	if controllerutil.ContainsFinalizer(cnp, membershipFinalizer) {
		return nil
	}
	patch := client.MergeFrom(cnp.DeepCopy())
	controllerutil.AddFinalizer(cnp, membershipFinalizer)
	return r.Patch(ctx, cnp, patch)
}

// removeFinalizer drops the membership finalizer from the backing policy.
func (r *Reconciler) removeFinalizer(ctx context.Context, cnp *CiliumNetworkPolicy) error {
	patch := client.MergeFrom(cnp.DeepCopy())
	controllerutil.RemoveFinalizer(cnp, membershipFinalizer)
	return r.Patch(ctx, cnp, patch)
}

// mapPodToSGs maps a managed-app pod to the SecurityGroups whose attachments
// include that pod's application, so a freshly-created pod is labeled promptly.
func (r *Reconciler) mapPodToSGs(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	// Only pods carrying an application identity can ever be members.
	if pod.Labels[appNameLabelKey] == "" {
		return nil
	}

	cnps := &CiliumNetworkPolicyList{}
	if err := r.List(ctx, cnps, client.InNamespace(pod.Namespace), client.MatchingLabels{sgLabelKey: sgLabelValue}); err != nil {
		// Returning no requests is the right signal here, but a transient List
		// error would otherwise be invisible — a pod update could be missed
		// until the next periodic resync with no trace. Log it.
		log.FromContext(ctx).Error(err, "failed to list SecurityGroup policies for pod mapping", "pod", pod.Name, "namespace", pod.Namespace)
		return nil
	}

	var reqs []reconcile.Request
	for i := range cnps.Items {
		for _, app := range decodeAttachments(ctx, cnps.Items[i].Annotations[attachmentsAnnotation]) {
			if appMatchesPod(app, pod.Labels) {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
					Namespace: cnps.Items[i].Namespace,
					Name:      cnps.Items[i].Name,
				}})
				break
			}
		}
	}
	return reqs
}

// appMatchesPod reports whether a pod carries all of an attachment's lineage
// labels.
func appMatchesPod(ref sdnv1alpha1.ApplicationReference, podLabels map[string]string) bool {
	for k, v := range appLabels(ref) {
		if podLabels[k] != v {
			return false
		}
	}
	return true
}

// SetupWithManager wires the controller: it reconciles marked
// CiliumNetworkPolicies and watches managed-app pods to enqueue the
// SecurityGroups they belong to.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	markerOnly := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[sgLabelKey] == sgLabelValue
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("securitygroup-membership").
		For(&CiliumNetworkPolicy{}, builder.WithPredicates(markerOnly)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapPodToSGs)).
		Complete(r)
}
