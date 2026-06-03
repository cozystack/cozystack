package fluxshardoperator

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// provisionRetry is the pause before re-checking a blocked step (flux
// Deployment not found yet, shard or legacy Deployment still draining).
const provisionRetry = 30 * time.Second

// ShardSetReconciler provisions one helm-controller Deployment per shard,
// cloned from the flux-aio "flux" Deployment's helm-controller container and
// sanitised for standalone use. The image and feature-gates are inherited from
// flux-aio automatically, so shards stay version-synced with no manual bump.
//
// It also prunes shard Deployments beyond ShardCount once they drain, and
// retires the legacy hand-rolled flux-tenants Deployment once no HelmRelease
// carries the legacy "tenants" shard key.
//
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
type ShardSetReconciler struct {
	client.Client
	Config *Config
}

// SetupWithManager registers the shard provisioner.
func (r *ShardSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toSingleton := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "shardset"}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("flux-shard-set").
		Watches(&appsv1.Deployment{}, toSingleton, builder.WithPredicates(r.relevantDeployment())).
		// HelmRelease label changes drive the drain checks (shard prune and
		// flux-tenants retirement).
		WatchesMetadata(HelmReleaseMeta(), toSingleton, builder.WithPredicates(metadataChanged())).
		Complete(r)
}

// relevantDeployment passes the flux-aio source Deployment, the legacy
// flux-tenants Deployment and the operator-managed shards.
func (r *ShardSetReconciler) relevantDeployment() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		if obj.GetNamespace() != r.Config.FluxNamespace {
			return false
		}
		if obj.GetName() == FluxDeploymentName || obj.GetName() == LegacyTenantsDeploymentName {
			return true
		}
		return obj.GetLabels()[ManagedByLabel] == ManagedByValue
	})
}

// Reconcile converges the shard runtime.
func (r *ShardSetReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	flux := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Namespace: r.Config.FluxNamespace, Name: FluxDeploymentName}, flux)
	if apierrors.IsNotFound(err) {
		logger.Info("flux Deployment not found yet, waiting", "namespace", r.Config.FluxNamespace)
		return ctrl.Result{RequeueAfter: provisionRetry}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting flux Deployment: %w", err)
	}

	shardCount, err := r.effectiveShardCount(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	for i := 0; i < shardCount; i++ {
		desired, err := BuildShardDeployment(flux, i, r.Config)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Patch(ctx, desired, client.Apply, client.FieldOwner(FieldOwner), client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying %s: %w", desired.Name, err)
		}
	}

	blocked := false

	pruneBlocked, err := r.pruneExtraShards(ctx, shardCount)
	if err != nil {
		return ctrl.Result{}, err
	}
	blocked = blocked || pruneBlocked

	legacyBlocked, err := r.retireLegacy(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	blocked = blocked || legacyBlocked

	if blocked {
		return ctrl.Result{RequeueAfter: provisionRetry}, nil
	}
	return ctrl.Result{}, nil
}

// effectiveShardCount resolves the shard count for this sync: the configured
// value, or in auto mode the recommendation with hysteresis anchored on the
// currently provisioned shards (see Config.EffectiveShardCount).
func (r *ShardSetReconciler) effectiveShardCount(ctx context.Context) (int, error) {
	if !r.Config.AutoShardCount {
		return r.Config.EffectiveShardCount(0, 0, 0), nil
	}

	hrs := HelmReleaseMetaList()
	if err := r.List(ctx, hrs, client.HasLabels{ShardKeyLabel}); err != nil {
		return 0, fmt.Errorf("listing HelmReleases: %w", err)
	}
	tenants := map[string]struct{}{}
	for i := range hrs.Items {
		if tenantNS, ok := TenantNamespaceForHR(&hrs.Items[i]); ok {
			tenants[tenantNS] = struct{}{}
		}
	}

	deps := &appsv1.DeploymentList{}
	if err := r.List(ctx, deps, client.InNamespace(r.Config.FluxNamespace),
		client.MatchingLabels{ManagedByLabel: ManagedByValue}); err != nil {
		return 0, fmt.Errorf("listing shard Deployments: %w", err)
	}
	current := 0
	for i := range deps.Items {
		if idx, ok := ParseShardDeploymentIndex(deps.Items[i].Name); ok && idx+1 > current {
			current = idx + 1
		}
	}

	return r.Config.EffectiveShardCount(len(hrs.Items), len(tenants), current), nil
}

// pruneExtraShards deletes operator-managed shard Deployments beyond the
// effective shard count once no HelmRelease points at them anymore. Returns
// true while a drain is still in progress.
func (r *ShardSetReconciler) pruneExtraShards(ctx context.Context, shardCount int) (bool, error) {
	logger := log.FromContext(ctx)

	deps := &appsv1.DeploymentList{}
	if err := r.List(ctx, deps, client.InNamespace(r.Config.FluxNamespace),
		client.MatchingLabels{ManagedByLabel: ManagedByValue}); err != nil {
		return false, fmt.Errorf("listing shard Deployments: %w", err)
	}

	blocked := false
	for i := range deps.Items {
		dep := &deps.Items[i]
		idx, ok := ParseShardDeploymentIndex(dep.Name)
		if !ok || idx < shardCount {
			continue
		}
		drained, err := r.drained(ctx, ShardName(idx))
		if err != nil {
			return false, err
		}
		if !drained {
			blocked = true
			continue
		}
		logger.Info("pruning drained shard Deployment", "deployment", dep.Name)
		if err := client.IgnoreNotFound(r.Delete(ctx, dep)); err != nil {
			return false, fmt.Errorf("deleting %s: %w", dep.Name, err)
		}
	}
	return blocked, nil
}

// retireLegacy deletes the hand-rolled flux-tenants Deployment once the
// placement controller has relabeled every HelmRelease off the legacy
// "tenants" bucket. Returns true while the backfill is still draining it.
func (r *ShardSetReconciler) retireLegacy(ctx context.Context) (bool, error) {
	logger := log.FromContext(ctx)

	legacy := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Namespace: r.Config.FluxNamespace, Name: LegacyTenantsDeploymentName}, legacy)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("getting %s Deployment: %w", LegacyTenantsDeploymentName, err)
	}

	drained, err := r.drained(ctx, LegacyShardKey)
	if err != nil {
		return false, err
	}
	if !drained {
		return true, nil
	}
	logger.Info("no HelmReleases left on the legacy tenants shard, retiring Deployment",
		"deployment", LegacyTenantsDeploymentName)
	if err := client.IgnoreNotFound(r.Delete(ctx, legacy)); err != nil {
		return false, fmt.Errorf("deleting %s: %w", LegacyTenantsDeploymentName, err)
	}
	return false, nil
}

// drained reports whether no HelmRelease carries the given shard key anymore.
func (r *ShardSetReconciler) drained(ctx context.Context, shardKey string) (bool, error) {
	hrs := HelmReleaseMetaList()
	if err := r.List(ctx, hrs, client.MatchingLabels{ShardKeyLabel: shardKey}); err != nil {
		return false, fmt.Errorf("listing HelmReleases with %s=%s: %w", ShardKeyLabel, shardKey, err)
	}
	return len(hrs.Items) == 0, nil
}

// BuildShardDeployment clones the helm-controller container out of the
// flux-aio Deployment and sanitises it into a standalone single-shard
// Deployment:
//
//   - hostNetwork off (flux-aio is hostNetwork:true; N shard pods would
//     collide on host ports 9795/9796), DNS policy back to ClusterFirst;
//   - only the helm-controller container is kept out of the all-in-one pod;
//   - --events-addr is dropped (it points at the notification-controller on
//     localhost, which does not exist in a standalone pod);
//   - --watch-label-selector is replaced with this shard's key selector;
//   - --concurrent and resources come from the operator values;
//   - SOURCE_*_LOCALHOST env wiring and tolerations for bootstrapping
//     CNI-less nodes are flux-aio specifics and are removed (the hand-rolled
//     flux-tenants shard had neither).
func BuildShardDeployment(flux *appsv1.Deployment, idx int, cfg *Config) (*appsv1.Deployment, error) {
	var src *corev1.Container
	for i := range flux.Spec.Template.Spec.Containers {
		if flux.Spec.Template.Spec.Containers[i].Name == HelmControllerContainerName {
			src = &flux.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if src == nil {
		return nil, fmt.Errorf("container %q not found in Deployment %s/%s",
			HelmControllerContainerName, flux.Namespace, flux.Name)
	}

	podSpec := flux.Spec.Template.Spec.DeepCopy()
	hc := src.DeepCopy()

	selectorArg := "--watch-label-selector=" + ShardKeyLabel + "=" + ShardName(idx)
	concurrentArg := "--concurrent=" + strconv.Itoa(cfg.ShardConcurrent)
	args := make([]string, 0, len(hc.Args))
	haveSelector, haveConcurrent := false, false
	for _, arg := range hc.Args {
		switch {
		case strings.HasPrefix(arg, "--events-addr"):
			continue
		case strings.HasPrefix(arg, "--watch-label-selector"):
			args, haveSelector = append(args, selectorArg), true
		case strings.HasPrefix(arg, "--concurrent"):
			args, haveConcurrent = append(args, concurrentArg), true
		default:
			args = append(args, arg)
		}
	}
	if !haveSelector {
		args = append(args, selectorArg)
	}
	if !haveConcurrent {
		args = append(args, concurrentArg)
	}
	hc.Args = args

	env := make([]corev1.EnvVar, 0, len(hc.Env))
	for _, e := range hc.Env {
		switch e.Name {
		// Localhost cross-container wiring of the all-in-one pod.
		case "SOURCE_CONTROLLER_LOCALHOST", "SOURCE_WATCHER_LOCALHOST":
			continue
		// The installer injects the node-local apiserver endpoint (e.g. Talos
		// KubePrism localhost:7445) into hostNetwork workloads
		// (internal/fluxinstall injectKubernetesServiceEnv). The sanitised pod
		// is not hostNetwork, so it must fall back to the in-cluster defaults.
		case "KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT":
			continue
		}
		env = append(env, e)
	}
	hc.Env = env

	if cfg.ShardResources.Requests != nil || cfg.ShardResources.Limits != nil {
		hc.Resources = cfg.ShardResources
	}

	mounted := map[string]bool{}
	for _, m := range hc.VolumeMounts {
		mounted[m.Name] = true
	}
	volumes := make([]corev1.Volume, 0, len(podSpec.Volumes))
	for _, v := range podSpec.Volumes {
		if mounted[v.Name] {
			volumes = append(volumes, v)
		}
	}
	podSpec.Volumes = volumes

	podSpec.Containers = []corev1.Container{*hc}
	podSpec.InitContainers = nil
	podSpec.HostNetwork = false
	podSpec.DNSPolicy = corev1.DNSClusterFirst
	podSpec.Tolerations = nil

	name := ShardDeploymentName(idx)
	labels := map[string]string{
		"app.kubernetes.io/name":    name,
		"app.kubernetes.io/part-of": "flux",
		"sharding.fluxcd.io/role":   "shard",
		ManagedByLabel:              ManagedByValue,
	}
	if version := flux.Labels["app.kubernetes.io/version"]; version != "" {
		labels["app.kubernetes.io/version"] = version
	}

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cfg.FluxNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			// helm-controller runs with leader election disabled, so two pods
			// of one shard must never overlap during a rollout.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": name},
					Annotations: map[string]string{
						"cluster-autoscaler.kubernetes.io/safe-to-evict": "true",
						"prometheus.io/scrape":                           "true",
					},
				},
				Spec: *podSpec,
			},
		},
	}, nil
}
