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
	"encoding/json"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

func tenantHR(t *testing.T, appName, namespace string, quotas map[string]string) *helmv2.HelmRelease {
	t.Helper()
	values := map[string]any{}
	if len(quotas) > 0 {
		values["resourceQuotas"] = quotas
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tenantNamespacePrefix + appName,
			Namespace: namespace,
			Labels:    map[string]string{appsv1alpha1.ApplicationKindLabel: tenantKind},
		},
		Spec: helmv2.HelmReleaseSpec{Values: &apiextv1.JSON{Raw: raw}},
	}
}

func ns(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func chartQuota(namespace string, used map[string]string) *corev1.ResourceQuota {
	usedList := corev1.ResourceList{}
	for k, v := range used {
		usedList[corev1.ResourceName(k)] = resource.MustParse(v)
	}
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: chartQuotaName, Namespace: namespace},
		Status:     corev1.ResourceQuotaStatus{Used: usedList},
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
	err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: allocatedQuotaName}, rq)
	if err != nil {
		return "", false
	}
	q, ok := rq.Spec.Hard[corev1.ResourceName(resourceName)]
	if !ok {
		return "", false
	}
	return q.String(), true
}

// TestReconcile_SharedPool: foo (cpu 10) with an unbounded child bar. With no
// usage, both namespaces are clamped to the full shared budget of 10.
func TestReconcile_SharedPool(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		tenantHR(t, "bar", "tenant-foo", nil),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", nil),
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
		tenantHR(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		tenantHR(t, "bar", "tenant-foo", nil),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", map[string]string{"cpu": "4"}),     // foo using 4
		chartQuota("tenant-foo-bar", map[string]string{"cpu": "3"}), // bar using 3
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// foo clamp = 10 - bar's 3 = 7; bar clamp = 10 - foo's 4 = 6.
	if got, _ := allocatedHard(t, c, "tenant-foo", "cpu"); got != "7" {
		t.Fatalf("tenant-foo allocated cpu = %q, want 7", got)
	}
	if got, _ := allocatedHard(t, c, "tenant-foo-bar", "cpu"); got != "6" {
		t.Fatalf("tenant-foo-bar allocated cpu = %q, want 6", got)
	}
}

// TestReconcile_CarveOut: foo (cpu 10) with a bounded child bar (cpu 4). foo's
// own namespace is clamped to the residual 6; bar forms its own pool of 4.
func TestReconcile_CarveOut(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		tenantHR(t, "bar", "tenant-foo", map[string]string{"cpu": "4"}),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		chartQuota("tenant-foo", nil), chartQuota("tenant-foo-bar", nil),
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, _ := allocatedHard(t, c, "tenant-foo", "cpu"); got != "6" {
		t.Fatalf("tenant-foo allocated cpu = %q, want 6 (10 - 4 carve-out)", got)
	}
	if got, _ := allocatedHard(t, c, "tenant-foo-bar", "cpu"); got != "4" {
		t.Fatalf("tenant-foo-bar allocated cpu = %q, want 4", got)
	}
}

// TestReconcile_Buffer: a 120% upgrade buffer inflates the enforced clamp.
func TestReconcile_Buffer(t *testing.T) {
	r, c := newReconciler(t,
		tenantHR(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		ns("tenant-foo"),
		chartQuota("tenant-foo", nil),
	)
	r.BufferPercent = 120
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, _ := allocatedHard(t, c, "tenant-foo", "cpu"); got != "12" {
		t.Fatalf("tenant-foo allocated cpu = %q, want 12 (10 * 120%%)", got)
	}
}

// TestReconcile_GarbageCollect: once a tenant gains its own quota it leaves the
// parent pool, and a previously-written allocated quota in an unrelated
// namespace is removed.
func TestReconcile_GarbageCollect(t *testing.T) {
	stale := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      allocatedQuotaName,
			Namespace: "tenant-gone",
			Labels:    map[string]string{managedByLabel: managedByValue},
		},
	}
	r, c := newReconciler(t,
		tenantHR(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		ns("tenant-foo"), ns("tenant-gone"),
		chartQuota("tenant-foo", nil),
		stale,
	)
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	rq := &corev1.ResourceQuota{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-gone", Name: allocatedQuotaName}, rq)
	if err == nil {
		t.Fatalf("stale allocated quota in tenant-gone should have been garbage-collected")
	}
}
