package fluxshardoperator

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// movesPerSync caps how many tenants change shards in one reconcile so a
	// backfill or rebalance never floods a target shard with an ADD burst.
	movesPerSync = 5
	// requeueShort is the pause between paced move batches.
	requeueShort = 15 * time.Second
	// resyncPeriod is the steady-state drift check interval.
	resyncPeriod = time.Minute
	// rebalanceCooldown is the per-tenant pause between rebalance moves so the
	// controller cannot thrash a tenant back and forth.
	rebalanceCooldown = 10 * time.Minute
)

// PlacementReconciler owns the tenant->shard assignment. It watches tenant
// HelmReleases and namespaces as metadata only, rebuilds its view from labels
// on every sync (restart-safe by construction), records each tenant's
// assignment on its namespace, and self-heals HelmRelease shard labels to
// match.
//
// All events collapse into one singleton reconcile request, so a noisy stream
// of label changes dedupes into sequential full syncs over ~10^3 metadata
// stubs.
//
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;patch
type PlacementReconciler struct {
	client.Client
	Config *Config

	// now is the clock, replaceable in tests.
	now func() time.Time
	// lastMoved gates rebalance moves per tenant. In-memory only: after a
	// restart the worst case is one rebalance move happening sooner than the
	// cooldown, which is harmless.
	lastMoved map[string]time.Time
}

// SetupWithManager registers the placement controller.
func (r *PlacementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.now == nil {
		r.now = time.Now
	}
	r.lastMoved = map[string]time.Time{}

	toSingleton := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "placement"}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("flux-shard-placement").
		WatchesMetadata(HelmReleaseMeta(), toSingleton, builder.WithPredicates(metadataChanged())).
		WatchesMetadata(NamespaceMeta(), toSingleton, builder.WithPredicates(tenantNamespace(), metadataChanged())).
		// Shard Deployment readiness gates reassignments; react to it directly
		// instead of waiting for the next requeue.
		Watches(&appsv1.Deployment{}, toSingleton, builder.WithPredicates(r.shardDeployment())).
		Complete(r)
}

// shardDeployment passes operator-managed shard Deployments.
func (r *PlacementReconciler) shardDeployment() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == r.Config.FluxNamespace &&
			obj.GetLabels()[ManagedByLabel] == ManagedByValue
	})
}

// metadataChanged ignores updates that change neither labels nor deletion
// state — in particular the resourceVersion bumps from helm-controller's
// status-patch firehose.
func metadataChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return true
			}
			return !maps.Equal(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels()) ||
				e.ObjectOld.GetDeletionTimestamp().IsZero() != e.ObjectNew.GetDeletionTimestamp().IsZero()
		},
	}
}

// tenantNamespace passes only tenant namespaces.
func tenantNamespace() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return strings.HasPrefix(obj.GetName(), "tenant-")
	})
}

// tenantView is the gathered state of one tenant.
type tenantView struct {
	info     TenantInfo
	hrs      []*metav1.PartialObjectMetadata
	nsExists bool
	nsLabel  string
}

// Reconcile performs one full placement sync.
func (r *PlacementReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	views, err := r.gather(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	tenants := make([]TenantInfo, 0, len(views))
	totalHRs := 0
	for _, v := range views {
		tenants = append(tenants, v.info)
		totalHRs += v.info.Weight
	}

	desired := ComputePlacement(PlacementInput{
		Tenants:            tenants,
		ShardCount:         r.Config.ShardCount,
		Pinned:             r.Config.PinnedTenants,
		RebalanceThreshold: r.Config.RebalanceThreshold,
		CanRebalance: func(tenant string) bool {
			return r.now().Sub(r.lastMoved[tenant]) >= rebalanceCooldown
		},
	})

	readyShards, err := r.readyShards(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	pending, errs := r.apply(ctx, views, desired, readyShards)

	r.report(views, desired, totalHRs, pending)
	r.pruneCooldowns(views)

	if len(errs) > 0 {
		return ctrl.Result{}, utilerrors.NewAggregate(errs)
	}
	if pending > 0 {
		logger.V(1).Info("placement sync paced", "pendingMoves", pending)
		return ctrl.Result{RequeueAfter: requeueShort}, nil
	}
	return ctrl.Result{RequeueAfter: resyncPeriod}, nil
}

// gather lists tenant HelmReleases and namespaces (metadata only) and groups
// them per tenant.
func (r *PlacementReconciler) gather(ctx context.Context) (map[string]*tenantView, error) {
	hrs := HelmReleaseMetaList()
	if err := r.List(ctx, hrs, client.HasLabels{ShardKeyLabel}); err != nil {
		return nil, fmt.Errorf("listing HelmReleases: %w", err)
	}
	nss := NamespaceMetaList()
	if err := r.List(ctx, nss); err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}

	nsByName := make(map[string]*metav1.PartialObjectMetadata, len(nss.Items))
	for i := range nss.Items {
		nsByName[nss.Items[i].Name] = &nss.Items[i]
	}

	views := map[string]*tenantView{}
	for i := range hrs.Items {
		hr := &hrs.Items[i]
		tenantNS, ok := TenantNamespaceForHR(hr)
		if !ok {
			continue
		}
		v := views[tenantNS]
		if v == nil {
			v = &tenantView{info: TenantInfo{Namespace: tenantNS}}
			views[tenantNS] = v
		}
		v.hrs = append(v.hrs, hr)
		v.info.Weight++
	}

	for tenantNS, v := range views {
		if ns := nsByName[tenantNS]; ns != nil {
			v.nsExists = true
			v.info.Deleting = !ns.DeletionTimestamp.IsZero()
			v.nsLabel = ns.Labels[TenantShardLabel]
		}
		// The recorded assignment (namespace label) wins; HR labels are the
		// fallback source of truth when no assignment is recorded yet.
		if _, ok := ParseShardIndex(v.nsLabel); ok {
			v.info.Current = v.nsLabel
		} else {
			v.info.Current = majorityShard(v.hrs)
		}
	}
	return views, nil
}

// majorityShard returns the most common canonical shard value among the
// tenant's HelmRelease labels, tie-break lowest shard index. Legacy "tenants"
// and unparseable values count as unassigned.
func majorityShard(hrs []*metav1.PartialObjectMetadata) string {
	counts := map[int]int{}
	for _, hr := range hrs {
		if idx, ok := ParseShardIndex(hr.GetLabels()[ShardKeyLabel]); ok {
			counts[idx]++
		}
	}
	best, bestCount := -1, 0
	for idx, c := range counts {
		if c > bestCount || (c == bestCount && best >= 0 && idx < best) {
			best, bestCount = idx, c
		}
	}
	if best < 0 {
		return ""
	}
	return ShardName(best)
}

// readyShards reports which shard Deployments currently have a ready replica.
// Reassignments only target ready shards, so a backfill can never hand
// tenants to a shard whose helm-controller is not actually running (e.g.
// crashlooping after a bad clone) — the safe-online-migration guarantee.
func (r *PlacementReconciler) readyShards(ctx context.Context) (map[string]bool, error) {
	deps := &appsv1.DeploymentList{}
	if err := r.List(ctx, deps, client.InNamespace(r.Config.FluxNamespace),
		client.MatchingLabels{ManagedByLabel: ManagedByValue}); err != nil {
		return nil, fmt.Errorf("listing shard Deployments: %w", err)
	}
	ready := map[string]bool{}
	for i := range deps.Items {
		if idx, ok := ParseShardDeploymentIndex(deps.Items[i].Name); ok {
			ready[ShardName(idx)] = deps.Items[i].Status.ReadyReplicas > 0
		}
	}
	return ready, nil
}

// apply pushes the desired assignment out, pacing tenant reassignments and
// self-healing label stragglers without limit. Reassignments whose target
// shard is not ready are deferred. Returns the number of reassignments left
// for the next batch.
func (r *PlacementReconciler) apply(ctx context.Context, views map[string]*tenantView, desired map[string]string, readyShards map[string]bool) (int, []error) {
	logger := log.FromContext(ctx)

	names := make([]string, 0, len(views))
	for name := range views {
		names = append(names, name)
	}
	sort.Strings(names)

	budget := movesPerSync
	pending := 0
	var errs []error
	for _, tenantNS := range names {
		v := views[tenantNS]
		target := desired[tenantNS]
		if target == "" {
			continue
		}
		if v.info.Current != target {
			if budget == 0 || !readyShards[target] {
				pending++
				continue
			}
			budget--
			r.lastMoved[tenantNS] = r.now()
			movesCounter.Inc()
			logger.Info("reassigning tenant", "tenant", tenantNS, "from", v.info.Current, "to", target, "weight", v.info.Weight)
		}
		if err := r.stamp(ctx, v, target); err != nil {
			errs = append(errs, err)
		}
	}
	return pending, errs
}

// stamp records the assignment on the tenant namespace and relabels every
// HelmRelease of the tenant that does not carry it yet. Patches the namespace
// first so the webhook hands out the new shard before the HRs settle.
func (r *PlacementReconciler) stamp(ctx context.Context, v *tenantView, target string) error {
	var errs []error
	if v.nsExists && v.nsLabel != target {
		if err := r.patchLabel(ctx, NamespaceGVK, "", v.info.Namespace, TenantShardLabel, target); err != nil {
			errs = append(errs, fmt.Errorf("namespace %s: %w", v.info.Namespace, err))
		}
	}
	for _, hr := range v.hrs {
		if hr.GetLabels()[ShardKeyLabel] == target {
			continue
		}
		if err := r.patchLabel(ctx, HelmReleaseGVK, hr.GetNamespace(), hr.GetName(), ShardKeyLabel, target); err != nil {
			errs = append(errs, fmt.Errorf("helmrelease %s/%s: %w", hr.GetNamespace(), hr.GetName(), err))
		}
	}
	return utilerrors.NewAggregate(errs)
}

// patchLabel merge-patches a single label on an object, touching nothing else.
func (r *PlacementReconciler) patchLabel(ctx context.Context, gvk schema.GroupVersionKind, namespace, name, label, value string) error {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(gvk)
	obj.SetNamespace(namespace)
	obj.SetName(name)
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, label, value)
	err := r.Patch(ctx, obj, client.RawPatch(types.MergePatchType, []byte(patch)))
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// report refreshes the telemetry gauges.
func (r *PlacementReconciler) report(views map[string]*tenantView, desired map[string]string, totalHRs, pending int) {
	shardLoadGauge.Reset()
	load := map[string]int{}
	for i := 0; i < r.Config.ShardCount; i++ {
		load[ShardName(i)] = 0
	}
	for tenantNS, shard := range desired {
		load[shard] += views[tenantNS].info.Weight
	}
	for shard, l := range load {
		shardLoadGauge.WithLabelValues(shard).Set(float64(l))
	}
	tenantsGauge.Set(float64(len(views)))
	helmReleasesGauge.Set(float64(totalHRs))
	pendingMovesGauge.Set(float64(pending))
	recommendedShardsGauge.Set(float64(RecommendedShardCount(totalHRs, len(views))))
}

// pruneCooldowns drops cooldown entries for tenants that no longer exist.
func (r *PlacementReconciler) pruneCooldowns(views map[string]*tenantView) {
	for tenant := range r.lastMoved {
		if _, ok := views[tenant]; !ok {
			delete(r.lastMoved, tenant)
		}
	}
}
