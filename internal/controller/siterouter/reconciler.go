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
	"strings"
	"sync"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	"github.com/cozystack/cozystack/internal/siterouter/denyset"
	"github.com/cozystack/cozystack/internal/vyos"
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

	// cozystackConfigNamespace / cozystackConfigName locate the cluster-wide
	// cozystack ConfigMap the deny-set validation sources the pod/service/join
	// CIDRs from.
	cozystackConfigNamespace = "cozy-system"
	cozystackConfigName      = "cozystack"

	// Platform-values defaults for the cluster networks, used when the cozystack
	// ConfigMap is absent or a key is unset (packages/core/platform/values.yaml
	// networking.{podCIDR,serviceCIDR,joinCIDR}).
	defaultPodCIDR     = "10.244.0.0/16"
	defaultServiceCIDR = "10.96.0.0/16"
	defaultJoinCIDR    = "100.64.0.0/16"

	// ConfigMap keys the cozystack ConfigMap exposes the cluster CIDRs under.
	configKeyPodCIDR  = "ipv4-pod-cidr"
	configKeySvcCIDR  = "ipv4-svc-cidr"
	configKeyJoinCIDR = "ipv4-join-cidr"

	// remoteCIDRsValueKey is the HelmRelease spec.values key holding the tenant's
	// declared remote networks (the authoritative input, D7).
	remoteCIDRsValueKey = "remoteCIDRs"
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

	// APIReader is an uncached reader (mgr.GetAPIReader) used to read the
	// cluster-wide cozystack ConfigMap and the tenant Namespace without spinning
	// up cluster-scoped informers for ConfigMaps/Namespaces (those types are
	// deliberately absent from CacheByObject). When nil the cached Client is used
	// — the path unit tests take, where the fake client serves every read.
	APIReader client.Reader

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

	// VyOSClientFactory builds a VyOSClient for a resolved gateway endpoint and
	// API token (T06 config push / runtime poll). Nil selects
	// DefaultVyOSClientFactory (production, self-signed TLS + in-band token).
	// Tests inject a fake to avoid standing up an HTTPS server; see vyospush.go.
	VyOSClientFactory VyOSClientFactory

	// hashMu guards appliedHashes.
	hashMu sync.Mutex
	// appliedHashes caches, per instance HelmRelease, the config hash of the last
	// successful VyOS push, so a no-op reconcile makes no HTTP call. In-memory is
	// safe because leader election gives a single writer; see the config-hash
	// cache note in vyospush.go for the restart trade-off.
	appliedHashes map[types.NamespacedName]string
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

	// vc is the VyOS management client built by pushVyOSConfig once the gateway
	// pod IP and API token are known; the confirm and runtime-poll steps reuse it
	// so they need not re-read the token or rebuild the client. Nil until push
	// reaches that point.
	vc VyOSClient
	// ipsecObservations / bgpObservations are the latest runtime readings from the
	// guest (pollRuntimeState), produced as data for T09 (status) / T10 (metrics)
	// to consume; this task builds neither conditions nor metrics (D9).
	ipsecObservations []vyos.IPSecObservation
	bgpObservations   []vyos.BGPObservation
}

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=services;secrets;configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get
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

	// Mediation pipeline, in dependency order. The ordering is load-bearing:
	// routes must be programmed and the guest source filter must be up before port
	// security is relaxed (D8), and the VyOS config (which installs that filter)
	// must be pushed before we confirm it and relax the port. classify turns a
	// soft wait/Degraded (deny-set stays a hard error) into a paced requeue.
	if err := r.validateRemoteCIDRs(ctx, inst); err != nil { // T07: deny-set validation
		return r.classify(ctx, err)
	}
	if err := r.programNamespaceRoutes(ctx, inst); err != nil { // T07: kube-ovn return routes
		return r.classify(ctx, err)
	}
	if err := r.pushVyOSConfig(ctx, inst); err != nil { // T06: VyOS HTTPS API push
		return r.classify(ctx, err)
	}
	if err := r.confirmSourceFilterActive(ctx, inst); err != nil { // T08/T06: guest source guard up
		return r.classify(ctx, err)
	}
	if err := r.relaxGatewayPortSecurity(ctx, inst); err != nil { // T07: Ready-gated port_security relax
		return r.classify(ctx, err)
	}
	if err := r.pollRuntimeState(ctx, inst); err != nil { // T06: tunnel/BGP observations (data for T09/T10)
		// Runtime polling is best-effort: a transient query failure keeps the
		// previous observations rather than failing the whole reconcile.
		logger.Error(err, "VyOS runtime poll failed; keeping previous observations",
			"instance", inst.name, "namespace", inst.namespace)
	}
	if err := r.updateStatus(ctx, inst); err != nil { // T09: status surface
		return r.classify(ctx, err)
	}

	// Steady-state runtime poll: re-render + re-apply on drift and refresh the
	// tunnel/BGP observations without waiting for a spec change (T06).
	return ctrl.Result{RequeueAfter: runtimePollInterval}, nil
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

	// Decode the tenant inputs so cleanup knows which route entries this instance
	// programmed (removeNamespaceRoutes withdraws only its own dst entries). A
	// malformed spec.values must not wedge deletion: log and proceed with empty
	// inputs (the annotation stays untouched, the finalizer still releases).
	values, err := decodeValues(inst.hr)
	if err != nil {
		log.FromContext(ctx).Error(err, "ignoring malformed HelmRelease spec.values during cleanup")
		values = map[string]interface{}{}
	}
	inst.values = values

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

	// Drop the cached config hash so a later instance reusing this key re-applies.
	r.forgetAppliedHash(client.ObjectKeyFromObject(inst.hr))

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
// deny-set check is a pure helper shared with the admission plugin (D9/D10). A
// violation returns a reconcileError carrying reason denyset.ReasonInvalidRemoteCIDR
// and a message naming every offender and its colliding network, so T09/status
// surfaces it machine-readably and the route is never programmed (this runs
// before programNamespaceRoutes in the pipeline).
func (r *SiteRouterReconciler) validateRemoteCIDRs(ctx context.Context, inst *instance) error {
	cidrs := stringSlice(inst.values[remoteCIDRsValueKey])
	if len(cidrs) == 0 {
		return nil
	}
	clusters, err := r.clusterNetworks(ctx)
	if err != nil {
		return err
	}
	rejections := denyset.Validate(cidrs, clusters)
	if len(rejections) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(rejections))
	for _, rej := range rejections {
		msgs = append(msgs, rej.Message())
	}
	return &reconcileError{reason: denyset.ReasonInvalidRemoteCIDR, message: strings.Join(msgs, "; ")}
}

// programNamespaceRoutes writes the ovn.kubernetes.io/routes annotation on the
// tenant namespace (the kube-ovn webhook propagates it to pods on create) so the
// return path to remoteCIDRs points at the gateway. It merges this instance's
// entries (keyed by dst) into whatever the namespace already carries — a
// co-tenant site-router's routes are preserved — and applies only the annotation
// via server-side apply under a distinct FieldOwner, so it never clobbers other
// writers of the namespace's annotations (the package_reconciler idiom). It is a
// no-op until the gateway pod has a routable IP (the pod watch re-triggers the
// reconcile when the IP appears).
func (r *SiteRouterReconciler) programNamespaceRoutes(ctx context.Context, inst *instance) error {
	cidrs := stringSlice(inst.values[remoteCIDRsValueKey])
	if len(cidrs) == 0 {
		return nil
	}
	if inst.gatewayPod == nil || inst.gatewayPod.Status.PodIP == "" {
		// No next hop yet; the pod watch re-triggers once the IP is assigned.
		return nil
	}

	ns := &corev1.Namespace{}
	if err := r.reader().Get(ctx, types.NamespacedName{Name: inst.namespace}, ns); err != nil {
		return fmt.Errorf("get namespace %s: %w", inst.namespace, err)
	}
	merged, err := mergeRoutes(ns.Annotations[routesAnnotation], inst.gatewayPod.Status.PodIP, cidrs)
	if err != nil {
		return fmt.Errorf("merge routes for namespace %s: %w", inst.namespace, err)
	}
	if ns.Annotations[routesAnnotation] == merged {
		return nil // already programmed; nothing to apply
	}

	apply := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        inst.namespace,
			Annotations: map[string]string{routesAnnotation: merged},
		},
	}
	apply.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Namespace"))
	if err := r.Patch(ctx, apply, client.Apply, client.FieldOwner(routesFieldOwner), client.ForceOwnership); err != nil {
		return fmt.Errorf("apply routes annotation on namespace %s: %w", inst.namespace, err)
	}
	return nil
}

// pushVyOSConfig and confirmSourceFilterActive live in vyospush.go (T06).

// relaxGatewayPortSecurity patches ovn.kubernetes.io/port_security=false on the
// gateway virt-launcher pod only, with a single-key merge patch so no other pod
// and no other annotation is touched. OVN otherwise drops the guest's routed,
// non-pod-IP source addresses; kube-ovn v1.15.10 cannot scope a CIDR in
// allowed-address-pairs, so Phase 1 relaxes fully and relies on the guest
// tunnel-ingress source filter (T08) as the compensating control — which is why
// this runs only AFTER confirmSourceFilterActive in the reconcile pipeline (D8:
// never relax before the guard is up). The finalizer restores it on delete.
//
// port_security timing (D8, validated empirically in T13): the kube-ovn mutating
// webhook only inherits namespace annotations onto pods at CREATE
// (packages/system/kubeovn-webhook admission.go: it no-ops for non-Create), so
// this patches the annotation directly on the live gateway pod. Whether kube-ovn
// v1.15.10 reconciles the logical-switch-port port_security field from an
// annotation flip on an already-wired port — rather than only at port creation —
// is not certain from the in-repo sources. If T13 shows the live toggle does not
// take effect, the fallback is to bake ovn.kubernetes.io/port_security: "false"
// onto the VM's pod template in the chart (applied at virt-launcher creation)
// while this controller keeps the Ready gate and the finalizer restore. The
// controller does NOT trigger a pod roll in Phase 1 — that decision waits on the
// T13 finding.
func (r *SiteRouterReconciler) relaxGatewayPortSecurity(ctx context.Context, inst *instance) error {
	if inst.gatewayPod == nil {
		// No gateway pod yet; the pod watch re-triggers once it is scheduled.
		return nil
	}
	pod := inst.gatewayPod
	if pod.Annotations[portSecurityAnnotation] == portSecurityRelaxed {
		return nil // already relaxed
	}
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[portSecurityAnnotation] = portSecurityRelaxed
	if err := r.Patch(ctx, pod, patch); err != nil {
		return fmt.Errorf("relax port_security on gateway pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return nil
}

// updateStatus surfaces the instance's runtime state (tunnel/BGP readiness,
// programmed routes, validation errors) through the HelmRelease Ready condition
// and a WorkloadMonitor (D9).
func (r *SiteRouterReconciler) updateStatus(_ context.Context, _ *instance) error {
	// TODO(T09): reflect runtime state through Ready + WorkloadMonitor.
	return nil
}

// restorePortSecurity reverts the port_security relaxation on delete by removing
// the annotation the controller added (its absence restores OVN's default
// enforcing behaviour), with a single-key merge patch on the gateway pod only. A
// missing pod or an absent annotation is a clean no-op — the state is already
// restored. Errors are returned, never masked: leaving a stuck restore to drop
// the finalizer would strand a relaxed port.
func (r *SiteRouterReconciler) restorePortSecurity(ctx context.Context, inst *instance) error {
	if inst.gatewayPod == nil {
		return nil
	}
	pod := inst.gatewayPod
	if _, set := pod.Annotations[portSecurityAnnotation]; !set {
		return nil
	}
	patch := client.MergeFrom(pod.DeepCopy())
	delete(pod.Annotations, portSecurityAnnotation)
	if err := r.Patch(ctx, pod, patch); err != nil {
		return fmt.Errorf("restore port_security on gateway pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return nil
}

// removeNamespaceRoutes withdraws this instance's route entries from the tenant
// namespace annotation on delete, keyed by dst, leaving any co-tenant
// site-router's entries intact. When the last entry is removed the annotation
// key is dropped entirely. The withdrawal uses a single-key merge patch (the
// namespace's other annotations are untouched); a missing namespace or absent
// annotation is a clean no-op. Errors are returned, never masked with "|| true".
func (r *SiteRouterReconciler) removeNamespaceRoutes(ctx context.Context, inst *instance) error {
	cidrs := stringSlice(inst.values[remoteCIDRsValueKey])
	if len(cidrs) == 0 {
		return nil
	}

	ns := &corev1.Namespace{}
	if err := r.reader().Get(ctx, types.NamespacedName{Name: inst.namespace}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // namespace already gone; nothing to reclaim
		}
		return fmt.Errorf("get namespace %s: %w", inst.namespace, err)
	}
	current := ns.Annotations[routesAnnotation]
	if current == "" {
		return nil
	}
	reduced, err := removeRoutes(current, cidrs)
	if err != nil {
		return fmt.Errorf("remove routes for namespace %s: %w", inst.namespace, err)
	}
	if reduced == current {
		return nil // none of our entries were present
	}

	patch := client.MergeFrom(ns.DeepCopy())
	if reduced == emptyRoutes {
		delete(ns.Annotations, routesAnnotation)
	} else {
		ns.Annotations[routesAnnotation] = reduced
	}
	if err := r.Patch(ctx, ns, patch); err != nil {
		return fmt.Errorf("withdraw routes annotation on namespace %s: %w", inst.namespace, err)
	}
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

// --- Mediation helpers ----------------------------------------------------

// reconcileError carries a machine-readable reason alongside the human message
// so a reason set deep in a step (e.g. denyset.ReasonInvalidRemoteCIDR) survives
// up to Reconcile's return and, in T09, onto the instance's Ready condition. It
// satisfies error; T09 type-asserts it to read Reason().
//
// requeueAfter distinguishes a soft, self-healing wait/Degraded (a positive
// duration: classify turns it into ctrl.Result{RequeueAfter} with a nil error, so
// controller-runtime does not apply exponential backoff and the reconcile retries
// on the poll cadence) from a hard error (zero: classify returns it so the manager
// logs and backs off). Deny-set rejection leaves it zero — a spec fix, not a
// requeue, resolves it — which keeps the T07 admission-parity contract.
type reconcileError struct {
	reason       string
	message      string
	requeueAfter time.Duration
}

func (e *reconcileError) Error() string { return e.reason + ": " + e.message }

// Reason exposes the machine-readable reason for T09/status.
func (e *reconcileError) Reason() string { return e.reason }

// classify turns a step error into the reconcile result. A reconcileError with a
// positive requeueAfter is a soft, event-backed wait/Degraded: requeue on that
// cadence with no hard error (no backoff, no double-report — the failing step has
// already recorded any Event). Everything else is a hard error the manager logs
// and backs off on.
func (r *SiteRouterReconciler) classify(ctx context.Context, err error) (ctrl.Result, error) {
	var re *reconcileError
	if errors.As(err, &re) && re.requeueAfter > 0 {
		log.FromContext(ctx).V(1).Info("requeueing SiteRouter reconcile",
			"reason", re.reason, "message", re.message, "after", re.requeueAfter.String())
		return ctrl.Result{RequeueAfter: re.requeueAfter}, nil
	}
	return ctrl.Result{}, err
}

// reader returns the uncached APIReader when one is wired (production), else the
// cached Client (unit tests, where the fake client serves every read). It is
// used for the cluster ConfigMap and tenant Namespace reads, which are
// deliberately not cached (see CacheByObject).
func (r *SiteRouterReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// clusterNetworks resolves the deny-set's cluster networks: the pod/service/join
// CIDRs from the cozy-system/cozystack ConfigMap, falling back to the
// platform-values defaults when the ConfigMap or a key is absent. NodeCIDRs and
// LBPools are left empty for now — the node subnet is not exposed as a cluster
// fact (nodes are on the host network, and the ConfigMap has no nodeCIDR key) and
// the LB pools are admin-provisioned out of band, so neither is cleanly
// discoverable; the deny-set's empty-field-skipped contract makes this safe, and
// pod/service/join + the always-reserved link-local/loopback/default-route cover
// the cluster-traffic-blackhole cases. TODO(T07 follow-up): source NodeCIDRs from
// Node objects and LBPools from the LB pool config/flag if a clean signal appears.
func (r *SiteRouterReconciler) clusterNetworks(ctx context.Context) (denyset.ClusterNetworks, error) {
	nets := denyset.ClusterNetworks{
		PodCIDR:     defaultPodCIDR,
		ServiceCIDR: defaultServiceCIDR,
		JoinCIDR:    defaultJoinCIDR,
	}

	cm := &corev1.ConfigMap{}
	err := r.reader().Get(ctx, types.NamespacedName{Namespace: cozystackConfigNamespace, Name: cozystackConfigName}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nets, nil // fall back to the platform-values defaults
		}
		return nets, fmt.Errorf("get %s/%s ConfigMap: %w", cozystackConfigNamespace, cozystackConfigName, err)
	}
	if v := cm.Data[configKeyPodCIDR]; v != "" {
		nets.PodCIDR = v
	}
	if v := cm.Data[configKeySvcCIDR]; v != "" {
		nets.ServiceCIDR = v
	}
	if v := cm.Data[configKeyJoinCIDR]; v != "" {
		nets.JoinCIDR = v
	}
	return nets, nil
}

// stringSlice coerces a decoded spec.values field (a []interface{} of strings
// from JSON) into []string, dropping any non-string element. A nil or non-slice
// value yields nil.
func stringSlice(v interface{}) []string {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
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
