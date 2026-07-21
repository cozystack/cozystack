// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

// Package siterouter reconciles SiteRouter app instances.
//
// A SiteRouter instance is not a CRD: it is a Flux HelmRelease projection
// labeled apps.cozystack.io/application.kind=SiteRouter (release name
// site-router-<instance>). The chart owns VM materialization — the gateway
// VirtualMachine, its boot DataVolume, the tunnel LoadBalancer Service and the
// PSK / api-key Secrets — while this controller mediates the pieces the chart
// cannot: it validates the tunnel's remote CIDRs against the cluster networks,
// programs the kube-ovn return routes, relaxes the gateway pod's port security
// once the guest source filter is up, pushes the VyOS configuration over the
// management API, and surfaces status. All of those steps are laid out here as
// ordered stubs; the logic lands in T06 (VyOS push), T07 (kube-ovn mediation)
// and T09 (status).
//
// Discovery mirrors internal/securitygroupcontroller: the instance's inputs are
// read from the HelmRelease spec.values (authoritative, per decision D7) and the
// gateway VM's virt-launcher pod is found through the lineage labels the lineage
// webhook stamps on managed-app resources
// (apps.cozystack.io/application.{group,kind,name},
// internal.cozystack.io/managed-by-cozystack), scoped to the instance namespace.
package siterouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// Lineage labels stamped by the lineage webhook on managed-app resources
	// (HelmReleases and the workloads they render). Mirrored here so the
	// controller does not depend on internal/lineagecontrollerwebhook.
	appGroupLabelKey = "apps.cozystack.io/application.group"
	appKindLabelKey  = "apps.cozystack.io/application.kind"
	appNameLabelKey  = "apps.cozystack.io/application.name"

	// appGroup is the application group shared by every catalog app.
	appGroup = "apps.cozystack.io"

	// siteRouterKind is the application kind this controller reconciles.
	siteRouterKind = "SiteRouter"

	// releasePrefix is prepended to an instance name to form its HelmRelease
	// name; it matches release.prefix in the site-router ApplicationDefinition.
	releasePrefix = "site-router-"

	// vmNameLabel is the KubeVirt label the gateway virt-launcher pod carries
	// (value: the VM name, which equals the release name site-router-<instance>).
	vmNameLabel = "vm.kubevirt.io/name"

	// finalizer guards the SiteRouter HelmRelease so the controller can undo its
	// kube-ovn mediation before the instance disappears. Its cleanup (T07)
	// restores the gateway pod's port security and removes the namespace routes
	// in that order; see reconcileDelete.
	finalizer = "apps.cozystack.io/site-router-mediation"
)

// CacheByObject bounds the manager's informers. The controller only ever acts
// on SiteRouter HelmReleases and their gateway pods, so caching anything else is
// wasted memory in a busy cluster. A managed SiteRouter resource never loses its
// lineage labels, so scoping by them cannot hide an instance the controller must
// later reconcile.
//
// NOTE for T06/T07: the mediation steps will additionally read per-instance
// Secrets (PSK, api-key), the tunnel LoadBalancer Service, the tenant Namespace
// and the cozy-system/cozystack ConfigMap. Those types are intentionally NOT
// cached here — reading them through the cache would spin up cluster-wide
// informers for every Secret/Service/Namespace. Read them with the uncached
// APIReader (mgr.GetAPIReader) or add narrowly label-scoped ByObject entries
// once the chart guarantees a selectable label on them.
func CacheByObject() map[client.Object]cache.ByObject {
	siteRouterInstances := labels.SelectorFromSet(labels.Set{
		appKindLabelKey:  siteRouterKind,
		appGroupLabelKey: appGroup,
	})
	gatewayPods := labels.SelectorFromSet(labels.Set{
		appKindLabelKey: siteRouterKind,
	})
	return map[client.Object]cache.ByObject{
		&helmv2.HelmRelease{}: {Label: siteRouterInstances},
		&corev1.Pod{}:         {Label: gatewayPods},
	}
}

// SiteRouterReconciler reconciles SiteRouter app instances.
//
// The baseline fields are what every reconcile needs; T06/T07/T09 add their own
// (a VyOS client factory, the cluster-network deny-set source, a config-hash
// cache, etc.). Keep new dependencies as exported fields wired from main so the
// struct stays trivially constructable in tests.
type SiteRouterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// ManagementCIDR is the source CIDR allowed to reach the VyOS management API
	// (SSH 22 / HTTPS 443). It MUST agree with the chart's managementCIDR value
	// (both default to the cluster pod CIDR 10.244.0.0/16): the chart seeds the
	// first-boot management firewall from its value, and the controller re-stamps
	// the same rule over the VyOS API from this one. A drift between the two
	// silently locks the controller out of the router.
	ManagementCIDR string
	// AllowOpenManagement mirrors --allow-open-management: when true an empty
	// ManagementCIDR is tolerated (test environments only).
	AllowOpenManagement bool
}

// instance is the resolved reconcile context threaded through the step methods.
// T06/T07/T09 extend it with the inputs their steps need (parsed tunnel spec,
// resolved LB address, config hash, ...) so the step signatures stay stable.
type instance struct {
	// hr is the SiteRouter HelmRelease (the app-instance projection).
	hr *helmv2.HelmRelease
	// name is the bare instance name (HelmRelease name minus releasePrefix); it
	// equals the lineage apps.cozystack.io/application.name label value.
	name string
	// namespace is the tenant namespace the instance lives in.
	namespace string
	// values is the decoded HelmRelease spec.values — the authoritative tenant
	// input (D7).
	values map[string]interface{}
	// gatewayPod is the gateway VM's virt-launcher pod, or nil if it has not been
	// scheduled yet (a normal transient state early in an instance's life).
	gatewayPod *corev1.Pod
}

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=services;secrets;configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch

// Reconcile discovers a single SiteRouter instance and, in later tasks, brings
// its kube-ovn mediation, VyOS configuration and status to the desired state.
// The scaffold performs no mediation: it establishes the cleanup finalizer,
// discovers the gateway pod, logs what it found, and returns. Every mediation
// step below is a no-op stub filled in by T06/T07/T09.
func (r *SiteRouterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, req.NamespacedName, hr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Defence in depth: the watch predicate and cache selector already scope us
	// to SiteRouter instances, but a label flip could still deliver a foreign HR.
	if hr.Labels[appKindLabelKey] != siteRouterKind {
		return ctrl.Result{}, nil
	}

	inst := &instance{
		hr:        hr,
		name:      instanceName(hr),
		namespace: hr.Namespace,
	}

	// Deletion: run the ordered kube-ovn teardown (T07) before releasing the
	// finalizer. Cleanup errors are returned, never swallowed — masking them with
	// "|| true" would drop the finalizer while the gateway pod is still relaxed
	// and the namespace still carries stale routes.
	if !hr.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, inst)
	}

	if err := r.ensureFinalizer(ctx, hr); err != nil {
		return ctrl.Result{}, err
	}

	// Authoritative tenant inputs come from the HelmRelease spec.values (D7),
	// never from the rendered objects, to avoid racing chart re-renders.
	values, err := decodeValues(hr)
	if err != nil {
		// A malformed spec.values is a Flux/rendering defect, not something a
		// requeue will fix; log and treat inputs as empty rather than hot-loop.
		logger.Error(err, "ignoring malformed HelmRelease spec.values")
		values = map[string]interface{}{}
	}
	inst.values = values

	// Discover the gateway VM's virt-launcher pod via lineage labels scoped to
	// the instance namespace. A missing pod is normal while the VM is still
	// booting; the pod watch re-triggers this reconcile once it appears.
	pod, err := r.discoverGatewayPod(ctx, inst)
	if err != nil {
		return ctrl.Result{}, err
	}
	inst.gatewayPod = pod

	if pod == nil {
		logger.Info("reconciling SiteRouter instance; gateway pod not scheduled yet",
			"instance", inst.name, "namespace", inst.namespace)
	} else {
		logger.Info("reconciling SiteRouter instance; discovered gateway pod",
			"instance", inst.name, "namespace", inst.namespace,
			"pod", pod.Name, "phase", pod.Status.Phase, "podIP", pod.Status.PodIP)
	}

	// Mediation pipeline, in dependency order. Every call is a no-op stub today.
	// The ordering is load-bearing: routes must be programmed and the guest
	// source filter must be up before port security is relaxed (D8), and the
	// VyOS config (which installs that filter) must be pushed before we confirm
	// it and relax the port. T06/T07/T09 fill these in; keep the order.
	if err := r.validateRemoteCIDRs(ctx, inst); err != nil { // T07: deny-set validation
		return ctrl.Result{}, err
	}
	if err := r.programNamespaceRoutes(ctx, inst); err != nil { // T07: kube-ovn return routes
		return ctrl.Result{}, err
	}
	if err := r.pushVyOSConfig(ctx, inst); err != nil { // T06: VyOS HTTPS API push
		return ctrl.Result{}, err
	}
	if err := r.confirmSourceFilterActive(ctx, inst); err != nil { // T08/T06: guest source guard up
		return ctrl.Result{}, err
	}
	if err := r.relaxGatewayPortSecurity(ctx, inst); err != nil { // T07: Ready-gated port_security relax
		return ctrl.Result{}, err
	}
	if err := r.updateStatus(ctx, inst); err != nil { // T09: status surface
		return ctrl.Result{}, err
	}

	// No requeue in the scaffold. T06 adds the ~30s runtime poll that re-applies
	// VyOS config on drift and refreshes tunnel/BGP status.
	return ctrl.Result{}, nil
}

// reconcileDelete tears down the controller's kube-ovn mediation in reverse
// order of how Reconcile establishes it, then drops the finalizer. Both steps
// are no-op stubs until T07; the ordering (restore port security, then remove
// routes) and the fail-hard error handling are the contract T07 implements
// against.
func (r *SiteRouterReconciler) reconcileDelete(ctx context.Context, inst *instance) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(inst.hr, finalizer) {
		return ctrl.Result{}, nil
	}

	// Best-effort discovery so T07's cleanup has the gateway pod if it still
	// exists; a missing pod is fine (the VM may already be gone).
	pod, err := r.discoverGatewayPod(ctx, inst)
	if err != nil {
		return ctrl.Result{}, err
	}
	inst.gatewayPod = pod

	if err := r.restorePortSecurity(ctx, inst); err != nil { // T07: revert port_security relax
		return ctrl.Result{}, err
	}
	if err := r.removeNamespaceRoutes(ctx, inst); err != nil { // T07: withdraw kube-ovn routes
		return ctrl.Result{}, err
	}

	if err := r.removeFinalizer(ctx, inst.hr); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("released SiteRouter mediation",
		"instance", inst.name, "namespace", inst.namespace)
	return ctrl.Result{}, nil
}

// --- Reconcile step stubs -------------------------------------------------
//
// Each method below is a placeholder returning nil. They exist so the reconcile
// pipeline compiles and its ordering is fixed now; the owning task fills in the
// body. Adding inputs a step needs should go on the instance struct, not the
// method signature, so these signatures stay stable across T06/T07/T09.

// validateRemoteCIDRs rejects an instance whose tunnel remoteCIDRs overlap the
// cluster pod/service/join/node/link-local/LB-pool networks (the deny-set). The
// deny-set check is a pure helper shared with the admission plugin (D9/D10).
func (r *SiteRouterReconciler) validateRemoteCIDRs(_ context.Context, _ *instance) error {
	// TODO(T07): implement deny-set validation against cluster networks.
	return nil
}

// programNamespaceRoutes writes the ovn.kubernetes.io/routes annotation on the
// tenant namespace (the kube-ovn webhook propagates it to pods on create) so the
// return path to remoteCIDRs points at the gateway.
func (r *SiteRouterReconciler) programNamespaceRoutes(_ context.Context, _ *instance) error {
	// TODO(T07): server-side apply the routes annotation on the namespace.
	return nil
}

// pushVyOSConfig renders the routed VyOS configuration from the instance inputs
// and applies it atomically over the VyOS HTTPS API, skipping the call when the
// config hash is unchanged.
func (r *SiteRouterReconciler) pushVyOSConfig(_ context.Context, _ *instance) error {
	// TODO(T06): render + push VyOS config over the management API.
	return nil
}

// confirmSourceFilterActive verifies the guest tunnel-ingress source filter is
// live before any port-security relaxation, so traffic sourced outside
// remoteCIDRs is dropped by the router first (D8).
func (r *SiteRouterReconciler) confirmSourceFilterActive(_ context.Context, _ *instance) error {
	// TODO(T08/T06): confirm the guest source filter is active.
	return nil
}

// relaxGatewayPortSecurity patches ovn.kubernetes.io/port_security=false on the
// gateway pod only, gated on confirmSourceFilterActive having passed, so the
// router can forward traffic whose source IP is not its own.
func (r *SiteRouterReconciler) relaxGatewayPortSecurity(_ context.Context, _ *instance) error {
	// TODO(T07): Ready-gated port_security relaxation on the gateway pod.
	return nil
}

// updateStatus surfaces the instance's runtime state (tunnel/BGP readiness,
// programmed routes, validation errors) through the HelmRelease Ready condition
// and a WorkloadMonitor (D9).
func (r *SiteRouterReconciler) updateStatus(_ context.Context, _ *instance) error {
	// TODO(T09): reflect runtime state through Ready + WorkloadMonitor.
	return nil
}

// restorePortSecurity reverts the port_security relaxation on delete.
func (r *SiteRouterReconciler) restorePortSecurity(_ context.Context, _ *instance) error {
	// TODO(T07): restore ovn.kubernetes.io/port_security on the gateway pod.
	return nil
}

// removeNamespaceRoutes withdraws the routes annotation on delete.
func (r *SiteRouterReconciler) removeNamespaceRoutes(_ context.Context, _ *instance) error {
	// TODO(T07): remove the ovn.kubernetes.io/routes annotation.
	return nil
}

// --- Discovery helpers ----------------------------------------------------

// discoverGatewayPod finds the gateway VM's virt-launcher pod in the instance
// namespace via the lineage labels. It returns nil (not an error) when no pod is
// found yet. When several pods match — e.g. during a live migration — it prefers
// a Running one, falling back to the first, so callers see a stable target.
func (r *SiteRouterReconciler) discoverGatewayPod(ctx context.Context, inst *instance) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	sel := client.MatchingLabels{
		appKindLabelKey: siteRouterKind,
		appNameLabelKey: inst.name,
	}
	if err := r.List(ctx, pods, client.InNamespace(inst.namespace), sel); err != nil {
		return nil, fmt.Errorf("list gateway pods for %s/%s: %w", inst.namespace, inst.name, err)
	}
	if len(pods.Items) == 0 {
		return nil, nil
	}
	chosen := &pods.Items[0]
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			chosen = &pods.Items[i]
			break
		}
	}
	return chosen, nil
}

// decodeValues parses the authoritative tenant inputs from the HelmRelease
// spec.values. A nil/empty value yields an empty map.
func decodeValues(hr *helmv2.HelmRelease) (map[string]interface{}, error) {
	if hr.Spec.Values == nil || len(hr.Spec.Values.Raw) == 0 {
		return map[string]interface{}{}, nil
	}
	var v map[string]interface{}
	if err := json.Unmarshal(hr.Spec.Values.Raw, &v); err != nil {
		return nil, fmt.Errorf("decode HelmRelease spec.values: %w", err)
	}
	return v, nil
}

// instanceName derives the bare instance name from the HelmRelease name by
// stripping the release prefix; the result equals the lineage
// apps.cozystack.io/application.name label value used to find the gateway pod.
func instanceName(hr *helmv2.HelmRelease) string {
	if name := hr.Labels[appNameLabelKey]; name != "" {
		return name
	}
	if len(hr.Name) > len(releasePrefix) && hr.Name[:len(releasePrefix)] == releasePrefix {
		return hr.Name[len(releasePrefix):]
	}
	return hr.Name
}

// --- Finalizer helpers ----------------------------------------------------

// ensureFinalizer adds the mediation finalizer to the instance HelmRelease via a
// merge patch (never an Update, which would race Flux's own writes to the HR).
func (r *SiteRouterReconciler) ensureFinalizer(ctx context.Context, hr *helmv2.HelmRelease) error {
	if controllerutil.ContainsFinalizer(hr, finalizer) {
		return nil
	}
	patch := client.MergeFrom(hr.DeepCopy())
	controllerutil.AddFinalizer(hr, finalizer)
	return r.Patch(ctx, hr, patch)
}

// removeFinalizer drops the mediation finalizer from the instance HelmRelease.
func (r *SiteRouterReconciler) removeFinalizer(ctx context.Context, hr *helmv2.HelmRelease) error {
	patch := client.MergeFrom(hr.DeepCopy())
	controllerutil.RemoveFinalizer(hr, finalizer)
	return r.Patch(ctx, hr, patch)
}

// --- Watches --------------------------------------------------------------

// mapPodToInstance maps a gateway virt-launcher pod to its SiteRouter instance
// so a pod appearing (or changing IP/phase) re-triggers the owning instance's
// reconcile. The reconcile key is the instance HelmRelease
// (site-router-<instance>) in the pod's namespace.
func (r *SiteRouterReconciler) mapPodToInstance(_ context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	if pod.Labels[appKindLabelKey] != siteRouterKind {
		return nil
	}
	name := pod.Labels[appNameLabelKey]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      releasePrefix + name,
	}}}
}

// SetupWithManager wires the controller. The primary trigger is the SiteRouter
// HelmRelease itself, filtered to the app-kind label — this is the reconcile
// unit. It is NOT a Flux storm: the informer cache is scoped to SiteRouter
// instances (CacheByObject), the watch predicate drops everything else, and the
// controller never writes the HelmRelease spec (only its finalizer + status), so
// it does not fight helm-controller. The secondary Pod watch re-triggers the
// instance when its gateway virt-launcher pod appears or changes.
func (r *SiteRouterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	siteRouterOnly := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[appKindLabelKey] == siteRouterKind
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("site-router").
		For(&helmv2.HelmRelease{}, builder.WithPredicates(siteRouterOnly)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapPodToInstance)).
		Complete(r)
}

// ValidateManagementCIDR enforces the fail-closed management-CIDR policy shared
// by the controller flags. An empty CIDR is rejected unless open management is
// explicitly allowed; a non-empty CIDR must parse. Factored out of main so the
// policy is unit-testable without triggering os.Exit.
func ValidateManagementCIDR(managementCIDR string, allowOpenManagement bool) error {
	if managementCIDR == "" {
		if allowOpenManagement {
			return nil
		}
		return errors.New("--management-cidr is required: the VyOS management API (SSH 22, HTTPS 443) " +
			"is otherwise reachable from anything that can route to the gateway VM; " +
			"pass --allow-open-management to opt out in a test environment")
	}
	if _, _, err := net.ParseCIDR(managementCIDR); err != nil {
		return fmt.Errorf("--management-cidr %q is not a valid CIDR: %w", managementCIDR, err)
	}
	return nil
}
