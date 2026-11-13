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
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

// tenantHR builds a tenant HelmRelease named "<prefix><app>" in namespace,
// carrying the kind label the controller selects on. Its values are irrelevant
// to the controller (the declared budget is read from the chart-rendered
// ResourceQuota, not the values), so they are omitted.
func tenantHR(appName, namespace string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tenantNamespacePrefix + appName,
			Namespace: namespace,
			Labels:    map[string]string{appsv1alpha1.ApplicationKindLabel: tenantKind},
		},
	}
}

func ns(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func resourceList(pairs map[string]string) corev1.ResourceList {
	if pairs == nil {
		return nil
	}
	out := corev1.ResourceList{}
	for k, v := range pairs {
		out[corev1.ResourceName(k)] = resource.MustParse(v)
	}
	return out
}

// chartQuota builds the chart-rendered "tenant-quota" ResourceQuota for a
// bounded tenant: spec.hard is the declared budget, status.used the current
// usage.
func chartQuota(namespace string, hard, used map[string]string) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: chartQuotaName, Namespace: namespace},
		Spec:       corev1.ResourceQuotaSpec{Hard: resourceList(hard)},
		Status:     corev1.ResourceQuotaStatus{Used: resourceList(used)},
	}
}

// usageQuota seeds an arbitrary ResourceQuota that only reports usage, used to
// give an unbounded (no chart-quota) member some current usage.
func usageQuota(namespace, name string, used map[string]string) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status:     corev1.ResourceQuotaStatus{Used: resourceList(used)},
	}
}

func newReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("helmv2 scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Reconciler{Client: c, Scheme: scheme}, c
}

func allocatedHard(t *testing.T, c client.Client, namespace, resourceName string) (string, bool) {
	t.Helper()
	rq := &corev1.ResourceQuota{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: allocatedQuotaName}, rq); err != nil {
		return "", false
	}
	q, ok := rq.Spec.Hard[corev1.ResourceName(resourceName)]
	if !ok {
		return "", false
	}
	return q.String(), true
}

// TestReconcile_LoneTenantIsNoOp: a tenant with a quota but no sub-tenants is
// already fully enforced by its chart quota, so the controller writes nothing.
func TestReconcile_LoneTenantIsNoOp(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		ns("tenant-foo"),
		chartQuota("tenant-foo", map[string]string{"cpu": "10"}, nil),
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := allocatedHard(t, c, "tenant-foo", "cpu"); ok {
		t.Fatalf("lone tenant must not get a controller-owned allocated quota")
	}
}

// TestReconcile_SharedPool: foo (cpu 10) with an unbounded child bar. They share
// the budget, so both members get an allocated quota clamping them to it.
func TestReconcile_SharedPool(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"cpu": "10"}, nil),
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, ok := allocatedHard(t, c, "tenant-foo", "cpu"); !ok || got != "10" {
		t.Fatalf("tenant-foo allocated cpu = %q (ok=%v), want 10", got, ok)
	}
	if got, ok := allocatedHard(t, c, "tenant-foo-bar", "cpu"); !ok || got != "10" {
		t.Fatalf("tenant-foo-bar allocated cpu = %q (ok=%v), want 10", got, ok)
	}
}

// TestReconcile_SharedPoolWithUsage: usage in one member shrinks the other
// member's clamp so their sum stays within the pool budget.
func TestReconcile_SharedPoolWithUsage(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"cpu": "10"}, map[string]string{"cpu": "4"}),
		usageQuota("tenant-foo-bar", "some-quota", map[string]string{"cpu": "3"}),
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, _ := allocatedHard(t, c, "tenant-foo", "cpu"); got != "7" { // 10 - bar's 3
		t.Fatalf("tenant-foo allocated cpu = %q, want 7", got)
	}
	if got, _ := allocatedHard(t, c, "tenant-foo-bar", "cpu"); got != "6" { // 10 - foo's 4
		t.Fatalf("tenant-foo-bar allocated cpu = %q, want 6", got)
	}
}

// TestReconcile_CarveOut: foo (cpu 10) with a bounded child bar (cpu 4). foo's
// own namespace is clamped to the residual 6; bar is a lone pool already
// enforced by its own chart quota, so it gets nothing extra.
func TestReconcile_CarveOut(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"cpu": "10"}, nil),
		chartQuota("tenant-foo-bar", map[string]string{"cpu": "4"}, nil),
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, _ := allocatedHard(t, c, "tenant-foo", "cpu"); got != "6" { // 10 - 4 carve-out
		t.Fatalf("tenant-foo allocated cpu = %q, want 6", got)
	}
	if _, ok := allocatedHard(t, c, "tenant-foo-bar", "cpu"); ok {
		t.Fatalf("bounded lone child tenant-foo-bar must not get an allocated quota")
	}
}

// TestReconcile_Buffer: the upgrade buffer inflates the enforced clamp.
func TestReconcile_Buffer(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"cpu": "10"}, nil),
	)
	r.BufferPercent = 120
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, _ := allocatedHard(t, c, "tenant-foo", "cpu"); got != "12" { // 10 * 120%
		t.Fatalf("tenant-foo allocated cpu = %q, want 12", got)
	}
}

// TestReconcile_GarbageCollect: a stale allocated quota in a namespace that is
// no longer enforced is removed.
func TestReconcile_GarbageCollect(t *testing.T) {
	stale := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      allocatedQuotaName,
			Namespace: "tenant-gone",
			Labels:    map[string]string{managedByLabel: managedByValue},
		},
	}
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		ns("tenant-foo"), ns("tenant-gone"),
		chartQuota("tenant-foo", map[string]string{"cpu": "10"}, nil),
		stale,
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	rq := &corev1.ResourceQuota{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-gone", Name: allocatedQuotaName}, rq); err == nil {
		t.Fatalf("stale allocated quota in tenant-gone should have been garbage-collected")
	}
}

// chartLimitRange builds the chart-rendered "tenant-range-limits" LimitRange,
// which the tenant chart emits alongside its "tenant-quota".
func chartLimitRange(namespace, defaultCPU string) *corev1.LimitRange {
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: chartLimitRangeName, Namespace: namespace},
		Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type:           corev1.LimitTypeContainer,
			Default:        resourceList(map[string]string{"cpu": defaultCPU, "memory": "128Mi"}),
			DefaultRequest: resourceList(map[string]string{"cpu": "25m", "memory": "128Mi"}),
		}}},
	}
}

func getLimitRange(c client.Client, namespace, name string) (*corev1.LimitRange, bool) {
	lr := &corev1.LimitRange{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, lr); err != nil {
		return nil, false
	}
	return lr, true
}

// TestReconcile_UnboundedMemberGetsPoolLimitRange: an unbounded child renders
// no chart quota and therefore no chart LimitRange, yet the controller clamps
// it with an allocated quota. Without container defaults, every pod that omits
// a limit the quota bounds (e.g. a sidecar without a CPU limit) is rejected at
// admission, so the controller must copy the pool root's LimitRange into it.
func TestReconcile_UnboundedMemberGetsPoolLimitRange(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"limits.cpu": "10"}, nil),
		chartLimitRange("tenant-foo", "250m"),
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	lr, ok := getLimitRange(c, "tenant-foo-bar", allocatedLimitRangeName)
	if !ok {
		t.Fatalf("unbounded member tenant-foo-bar has an allocated quota but no LimitRange")
	}
	if len(lr.Spec.Limits) != 1 {
		t.Fatalf("tenant-foo-bar LimitRange limits = %d, want 1", len(lr.Spec.Limits))
	}
	if got := lr.Spec.Limits[0].Default[corev1.ResourceCPU]; got.String() != "250m" {
		t.Fatalf("tenant-foo-bar default cpu = %q, want 250m copied from the pool root", got.String())
	}
	if lr.Labels[managedByLabel] != managedByValue {
		t.Fatalf("tenant-foo-bar LimitRange is not marked as controller-owned")
	}
	if _, ok := getLimitRange(c, "tenant-foo", allocatedLimitRangeName); ok {
		t.Fatalf("pool root tenant-foo already has the chart LimitRange and must not get a second one")
	}
}

// TestReconcile_PoolLimitRangeFollowsRoot: a change to the root's chart
// LimitRange propagates to the members on the next sweep.
func TestReconcile_PoolLimitRangeFollowsRoot(t *testing.T) {
	root := chartLimitRange("tenant-foo", "250m")
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"limits.cpu": "10"}, nil),
		root,
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	root.Spec.Limits[0].Default[corev1.ResourceCPU] = resource.MustParse("500m")
	if err := c.Update(context.Background(), root); err != nil {
		t.Fatalf("update root LimitRange: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	lr, ok := getLimitRange(c, "tenant-foo-bar", allocatedLimitRangeName)
	if !ok {
		t.Fatalf("tenant-foo-bar lost its LimitRange")
	}
	if got := lr.Spec.Limits[0].Default[corev1.ResourceCPU]; got.String() != "500m" {
		t.Fatalf("tenant-foo-bar default cpu = %q, want 500m after the root changed", got.String())
	}
}

// TestReconcile_PoolLimitRangeGarbageCollected: once the child declares its own
// quota, the chart renders its LimitRange and the controller-owned copy goes.
func TestReconcile_PoolLimitRangeGarbageCollected(t *testing.T) {
	stale := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      allocatedLimitRangeName,
			Namespace: "tenant-foo-bar",
			Labels:    map[string]string{managedByLabel: managedByValue},
		},
	}
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"limits.cpu": "10"}, nil),
		chartLimitRange("tenant-foo", "250m"),
		chartQuota("tenant-foo-bar", map[string]string{"limits.cpu": "4"}, nil),
		chartLimitRange("tenant-foo-bar", "250m"),
		stale,
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := getLimitRange(c, "tenant-foo-bar", allocatedLimitRangeName); ok {
		t.Fatalf("bounded child tenant-foo-bar renders its own LimitRange; the controller copy must be removed")
	}
	if _, ok := getLimitRange(c, "tenant-foo-bar", chartLimitRangeName); !ok {
		t.Fatalf("the chart-owned LimitRange must never be touched")
	}
}

// TestReconcile_PoolRootWithoutLimitRange: if the root has no chart LimitRange
// there is nothing to copy; the quota is still enforced and the sweep succeeds.
func TestReconcile_PoolRootWithoutLimitRange(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"cpu": "10"}, nil),
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := allocatedHard(t, c, "tenant-foo-bar", "cpu"); !ok {
		t.Fatalf("tenant-foo-bar must still get its allocated quota")
	}
	if _, ok := getLimitRange(c, "tenant-foo-bar", allocatedLimitRangeName); ok {
		t.Fatalf("no LimitRange can be copied from a root that has none")
	}
}

// TestReconcile_NoLimitRangeWithoutComputeLimitQuota: the chart renders its
// LimitRange for any non-empty resourceQuotas, compute or not. A member whose
// allocated quota bounds neither limits.cpu nor limits.memory is admitted
// without limits today; copying the root's defaults there would cap or refuse
// pods (one requesting more than the default memory limit, for instance)
// that the quota itself never constrains.
func TestReconcile_NoLimitRangeWithoutComputeLimitQuota(t *testing.T) {
	stale := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      allocatedLimitRangeName,
			Namespace: "tenant-foo-bar",
			Labels:    map[string]string{managedByLabel: managedByValue},
		},
	}
	r, c := newReconciler(t,
		tenantHR("foo", "tenant-root"),
		tenantHR("bar", "tenant-foo"),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"services.loadbalancers": "1"}, nil),
		chartLimitRange("tenant-foo", "250m"),
		stale,
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := allocatedHard(t, c, "tenant-foo-bar", "services.loadbalancers"); !ok {
		t.Fatalf("tenant-foo-bar must still get its allocated quota")
	}
	if _, ok := getLimitRange(c, "tenant-foo-bar", allocatedLimitRangeName); ok {
		t.Fatalf("tenant-foo-bar's quota bounds no compute limit; it must carry no LimitRange copy")
	}
}
