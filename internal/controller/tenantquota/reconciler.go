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

package tenantquota

import (
	"context"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const (
	// tenantKind gates which Applications participate in hierarchical quotas.
	tenantKind = "Tenant"
	// chartQuotaName is the per-namespace ResourceQuota the tenant chart renders
	// from tenant.spec.resourceQuotas (the tenant's declared budget).
	chartQuotaName = "tenant-quota"
	// allocatedQuotaName is the controller-owned ResourceQuota that tightens a
	// namespace down to its share of the parent pool. Kubernetes enforces the
	// most restrictive of all ResourceQuotas in a namespace, so this binds
	// whenever it is below the chart quota, without the controller fighting Flux
	// over the chart-owned object.
	allocatedQuotaName = "tenant-quota-allocated"
	// managedByLabel marks the controller-owned ResourceQuotas so they can be
	// listed and garbage-collected.
	managedByLabel = "quota.cozystack.io/managed-by"
	managedByValue = "tenant-quota-controller"
	// resyncInterval bounds how stale the shared-pool clamp can get if a usage
	// update is ever missed.
	resyncInterval = 60 * time.Second
)

// sweepKey coalesces every watch event into a single full-tree reconcile, since
// a quota pool spans many namespaces and any change can affect siblings.
var sweepKey = reconcile.Request{NamespacedName: types.NamespacedName{Name: "tenantquota-sweep"}}

// Reconciler enforces hierarchical tenant quotas at runtime. It is the
// reconciliation half of the design adapted from OpenShift's ClusterResourceQuota
// controllers (see the package doc); the declaration-time gate lives in the
// aggregated apiserver (pkg/registry/apps/application).
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// BufferPercent temporarily inflates every pool budget (e.g. 120 = +20%) so
	// that tenants already over a freshly-introduced quota keep running while
	// operators right-size. 0 or 100 disables the buffer.
	BufferPercent int64
}

func (r *Reconciler) bufferPercent() int64 {
	if r.BufferPercent <= 0 {
		return 100
	}
	return r.BufferPercent
}

// Reconcile rebuilds the whole tenant-quota picture: it reads every tenant's
// declared budget and current usage, computes the pools, and writes one
// controller-owned ResourceQuota per pool-member namespace clamping it to its
// fair share of the pool. Pools that need no tightening beyond the chart-owned
// quota (a lone tenant with no sub-tenants) are left untouched.
func (r *Reconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	tenants, usedByNS, existing, err := r.snapshot(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	pools := ComputePools(tenants)

	desired := map[string]corev1.ResourceList{}
	for _, p := range pools {
		if over := p.Overcommitted(); len(over) > 0 {
			logger.Info("tenant quota pool is overcommitted by sub-tenant carve-outs", "pool", p.Root, "overcommit", over)
			r.recordOvercommit(ctx, p.Root, over)
		}
		// A pool with no carve-outs and a single member is fully covered by the
		// chart-rendered tenant-quota already; the controller adds nothing.
		if len(p.CarvedOut) == 0 && len(p.Members) <= 1 {
			continue
		}
		buffered := &Pool{
			Root:      p.Root,
			Available: ScaleResourceList(p.Available, r.bufferPercent()),
			Members:   p.Members,
		}
		for _, ns := range p.Members {
			if existing[ns] {
				desired[ns] = buffered.EnforcedHard(ns, usedByNS)
			}
		}
	}

	for ns, hard := range desired {
		if err := r.upsertAllocatedQuota(ctx, ns, hard); err != nil {
			// A transient namespace race must not wedge the whole sweep.
			logger.Error(err, "failed to apply allocated quota", "namespace", ns)
		}
	}
	if err := r.gcAllocatedQuotas(ctx, desired); err != nil {
		logger.Error(err, "failed to garbage-collect stale allocated quotas")
	}

	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// snapshot reads the whole picture in one pass: the tenant namespaces (from
// tenant HelmReleases), each tenant's declared budget, current usage per
// namespace, and the set of existing namespaces.
//
// A tenant's declared budget is taken from its chart-rendered "tenant-quota"
// ResourceQuota (`.spec.hard`), not from the raw tenant.spec.resourceQuotas
// values. The chart expands the shorthand values into the real quota resource
// names and applies the cluster's request/limit allocation ratios
// (cozy-lib.resources.flatten); reading the rendered object keeps the
// controller's allocated quota in exactly the same key space, so the two
// quotas compose correctly and the controller never has to replicate that
// (cluster-configurable) transformation.
func (r *Reconciler) snapshot(ctx context.Context) (tenants []Tenant, usedByNS map[string]corev1.ResourceList, existing map[string]bool, err error) {
	releases := &helmv2.HelmReleaseList{}
	if err = r.List(ctx, releases, client.MatchingLabels{appsv1alpha1.ApplicationKindLabel: tenantKind}); err != nil {
		return nil, nil, nil, err
	}

	quotas := &corev1.ResourceQuotaList{}
	if err = r.List(ctx, quotas); err != nil {
		return nil, nil, nil, err
	}
	declaredByNS := map[string]corev1.ResourceList{}
	usedByNS = map[string]corev1.ResourceList{}
	for i := range quotas.Items {
		rq := &quotas.Items[i]
		// Multiple ResourceQuotas in a namespace each report the same usage for
		// a given resource, so usage is merged with a per-resource max (not a
		// sum) to avoid double counting.
		usedByNS[rq.Namespace] = maxResourceList(usedByNS[rq.Namespace], rq.Status.Used)
		if rq.Name == chartQuotaName {
			declaredByNS[rq.Namespace] = rq.Spec.Hard
		}
	}

	nsList := &corev1.NamespaceList{}
	if err = r.List(ctx, nsList); err != nil {
		return nil, nil, nil, err
	}
	existing = make(map[string]bool, len(nsList.Items))
	for i := range nsList.Items {
		existing[nsList.Items[i].Name] = true
	}

	tenants = make([]Tenant, 0, len(releases.Items))
	for i := range releases.Items {
		hr := &releases.Items[i]
		ns := ownedNamespace(hr.Namespace, strings.TrimPrefix(hr.Name, tenantNamespacePrefix))
		tenants = append(tenants, Tenant{Namespace: ns, Declared: declaredByNS[ns]})
	}
	return tenants, usedByNS, existing, nil
}

func (r *Reconciler) upsertAllocatedQuota(ctx context.Context, namespace string, hard corev1.ResourceList) error {
	rq := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: allocatedQuotaName, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rq, func() error {
		if rq.Labels == nil {
			rq.Labels = map[string]string{}
		}
		rq.Labels[managedByLabel] = managedByValue
		rq.Labels["internal.cozystack.io/managed-by-cozystack"] = ""
		rq.Spec.Hard = hard
		return nil
	})
	return err
}

// gcAllocatedQuotas deletes controller-owned ResourceQuotas in namespaces that
// are no longer pool members (e.g. a tenant gained its own quota, or was
// deleted).
func (r *Reconciler) gcAllocatedQuotas(ctx context.Context, desired map[string]corev1.ResourceList) error {
	managed := &corev1.ResourceQuotaList{}
	if err := r.List(ctx, managed, client.MatchingLabels{managedByLabel: managedByValue}); err != nil {
		return err
	}
	for i := range managed.Items {
		m := &managed.Items[i]
		if _, keep := desired[m.Namespace]; keep {
			continue
		}
		if err := r.Delete(ctx, m); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *Reconciler) recordOvercommit(ctx context.Context, rootNamespace string, over corev1.ResourceList) {
	if r.Recorder == nil {
		return
	}
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: rootNamespace}, ns); err != nil {
		return
	}
	r.Recorder.Eventf(ns, corev1.EventTypeWarning, "QuotaOvercommitted",
		"sub-tenant quotas exceed this tenant's budget by %s; lower a sub-tenant quota or raise this tenant's quota", resourceListString(over))
}

// SetupWithManager wires the controller. Every relevant change (a tenant
// HelmRelease, a namespace, or a tenant ResourceQuota whose usage moved)
// coalesces into one full-tree sweep.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	toSweep := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{sweepKey}
	})
	tenantReleases := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[appsv1alpha1.ApplicationKindLabel] == tenantKind
	})
	tenantQuotas := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == chartQuotaName || o.GetName() == allocatedQuotaName
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("tenantquota-controller").
		Watches(&helmv2.HelmRelease{}, toSweep, builder.WithPredicates(tenantReleases)).
		Watches(&corev1.Namespace{}, toSweep).
		Watches(&corev1.ResourceQuota{}, toSweep, builder.WithPredicates(tenantQuotas)).
		Complete(r)
}

// ownedNamespace maps a tenant HelmRelease (name "<prefix><app>" in namespace
// hrNamespace) to the namespace that tenant owns. It mirrors
// REST.computeTenantNamespace in the aggregated apiserver.
func ownedNamespace(hrNamespace, appName string) string {
	switch {
	case hrNamespace == rootTenantNamespace && appName == "root":
		return rootTenantNamespace
	case hrNamespace == rootTenantNamespace:
		return tenantNamespacePrefix + appName
	default:
		return hrNamespace + "-" + appName
	}
}

// maxResourceList returns the per-resource maximum of a and b.
func maxResourceList(a, b corev1.ResourceList) corev1.ResourceList {
	out := corev1.ResourceList{}
	for k, v := range a {
		out[k] = v.DeepCopy()
	}
	for k, v := range b {
		if cur, ok := out[k]; !ok || v.Cmp(cur) > 0 {
			out[k] = v.DeepCopy()
		}
	}
	return out
}

func resourceListString(rl corev1.ResourceList) string {
	parts := make([]string, 0, len(rl))
	for k, v := range rl {
		parts = append(parts, string(k)+"="+v.String())
	}
	return strings.Join(parts, ", ")
}
