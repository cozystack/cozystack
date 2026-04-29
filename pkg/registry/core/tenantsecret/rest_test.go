// SPDX-License-Identifier: Apache-2.0

package tenantsecret

import (
	"context"
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const testNamespace = "tenant-root"

func newTestREST(t *testing.T, secrets ...*corev1.Secret) *REST {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}

	objs := make([]client.Object, 0, len(secrets))
	for _, s := range secrets {
		objs = append(objs, s)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()

	return &REST{
		c: fc,
		w: fc,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenantsecrets",
		},
	}
}

// makeTenantSecret produces a Secret already labeled as a tenant resource plus any extra labels.
func makeTenantSecret(name string, extra map[string]string) *corev1.Secret {
	lbls := map[string]string{
		corev1alpha1.TenantResourceLabelKey: corev1alpha1.TenantResourceLabelValue,
	}
	for k, v := range extra {
		lbls[k] = v
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    lbls,
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func itemNames(items []corev1alpha1.TenantSecret) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	sort.Strings(out)
	return out
}

func listTenantSecrets(t *testing.T, r *REST, opts *metainternal.ListOptions) *corev1alpha1.TenantSecretList {
	t.Helper()
	ctx := request.WithNamespace(context.Background(), testNamespace)
	out, err := r.List(ctx, opts)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	list, ok := out.(*corev1alpha1.TenantSecretList)
	if !ok {
		t.Fatalf("expected *TenantSecretList, got %T", out)
	}
	return list
}

func TestList_NoSelector_ReturnsOnlyTenantSecrets(t *testing.T) {
	bucket := makeTenantSecret("bucket-creds", map[string]string{
		"apps.cozystack.io/application.kind": "Bucket",
		"apps.cozystack.io/application.name": "test",
	})
	monitoring := makeTenantSecret("monitoring-creds", map[string]string{
		"apps.cozystack.io/application.kind": "Monitoring",
		"apps.cozystack.io/application.name": "monitoring",
	})
	// Plain Secret without the tenant marker — must be excluded from the list.
	plain := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plain",
			Namespace: testNamespace,
		},
	}

	r := newTestREST(t, bucket, monitoring, plain)

	list := listTenantSecrets(t, r, &metainternal.ListOptions{})

	got := itemNames(list.Items)
	want := []string{"bucket-creds", "monitoring-creds"}
	if !equalStrings(got, want) {
		t.Fatalf("unexpected items: got %v, want %v", got, want)
	}
}

func TestList_WithLabelSelector_FiltersToMatchingApp(t *testing.T) {
	bucket := makeTenantSecret("bucket-creds", map[string]string{
		"apps.cozystack.io/application.kind": "Bucket",
		"apps.cozystack.io/application.name": "test",
	})
	monitoring := makeTenantSecret("monitoring-creds", map[string]string{
		"apps.cozystack.io/application.kind": "Monitoring",
		"apps.cozystack.io/application.name": "monitoring",
	})
	other := makeTenantSecret("other-bucket", map[string]string{
		"apps.cozystack.io/application.kind": "Bucket",
		"apps.cozystack.io/application.name": "other",
	})

	r := newTestREST(t, bucket, monitoring, other)

	sel, err := labels.Parse(
		"apps.cozystack.io/application.kind=Bucket,apps.cozystack.io/application.name=test",
	)
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	list := listTenantSecrets(t, r, &metainternal.ListOptions{LabelSelector: sel})

	got := itemNames(list.Items)
	want := []string{"bucket-creds"}
	if !equalStrings(got, want) {
		t.Fatalf("expected only bucket-creds, got %v", got)
	}
}

func TestList_WithLabelSelector_NoMatch_ReturnsEmpty(t *testing.T) {
	r := newTestREST(t, makeTenantSecret("bucket-creds", map[string]string{
		"apps.cozystack.io/application.kind": "Bucket",
		"apps.cozystack.io/application.name": "monitoring",
	}))

	sel, err := labels.Parse("apps.cozystack.io/application.name=does-not-exist")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	list := listTenantSecrets(t, r, &metainternal.ListOptions{LabelSelector: sel})

	if len(list.Items) != 0 {
		t.Fatalf("expected empty list, got %v", itemNames(list.Items))
	}
}

func TestList_WithLabelSelector_PreservesTenantFilter(t *testing.T) {
	// A non-tenant Secret carrying the same user labels must NOT leak through
	// the label-selector filter.
	leaked := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "leaked",
			Namespace: testNamespace,
			Labels: map[string]string{
				"apps.cozystack.io/application.kind": "Bucket",
				"apps.cozystack.io/application.name": "test",
			},
		},
	}
	bucket := makeTenantSecret("bucket-creds", map[string]string{
		"apps.cozystack.io/application.kind": "Bucket",
		"apps.cozystack.io/application.name": "test",
	})

	r := newTestREST(t, leaked, bucket)

	sel, _ := labels.Parse(
		"apps.cozystack.io/application.kind=Bucket,apps.cozystack.io/application.name=test",
	)

	list := listTenantSecrets(t, r, &metainternal.ListOptions{LabelSelector: sel})

	got := itemNames(list.Items)
	want := []string{"bucket-creds"}
	if !equalStrings(got, want) {
		t.Fatalf("non-tenant Secret leaked through filter: got %v, want %v", got, want)
	}
}

func TestList_WithEverythingSelector_BehavesLikeNoSelector(t *testing.T) {
	bucket := makeTenantSecret("bucket-creds", map[string]string{
		"apps.cozystack.io/application.name": "test",
	})
	monitoring := makeTenantSecret("monitoring-creds", map[string]string{
		"apps.cozystack.io/application.name": "monitoring",
	})

	r := newTestREST(t, bucket, monitoring)

	list := listTenantSecrets(t, r, &metainternal.ListOptions{LabelSelector: labels.Everything()})

	got := itemNames(list.Items)
	want := []string{"bucket-creds", "monitoring-creds"}
	if !equalStrings(got, want) {
		t.Fatalf("expected all tenant secrets, got %v", got)
	}
}

func TestList_WithNothingSelector_ReturnsEmpty(t *testing.T) {
	r := newTestREST(t, makeTenantSecret("bucket-creds", map[string]string{
		"apps.cozystack.io/application.kind": "Bucket",
	}))

	list := listTenantSecrets(t, r, &metainternal.ListOptions{LabelSelector: labels.Nothing()})

	if len(list.Items) != 0 {
		t.Fatalf("expected empty list for Nothing() selector, got %v", itemNames(list.Items))
	}
}

func TestWatch_WithNothingSelector_ClosesImmediately(t *testing.T) {
	r := newTestREST(t)

	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), testNamespace))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{LabelSelector: labels.Nothing()})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	select {
	case _, ok := <-w.ResultChan():
		if ok {
			t.Fatal("expected closed channel for Nothing() selector, got an event")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for channel to close")
	}
}

// TestWatch_WithLabelSelector_FiltersEvents reproduces issue
// cozystack/cozystack#2527: TenantSecret Watch ignored opts.LabelSelector
// and streamed every tenant Secret in the namespace, regardless of the
// user-provided selector.
//
// fake.Client.Watch does not emit initial ADDED events for objects already in
// the tracker — only events for subsequent CREATE/UPDATE/DELETE. So we start
// the watch first and then create two secrets; only the one matching the
// selector should be observed.
func TestWatch_WithLabelSelector_FiltersEvents(t *testing.T) {
	r := newTestREST(t)

	sel, err := labels.Parse(
		"apps.cozystack.io/application.kind=Harbor,apps.cozystack.io/application.name=test",
	)
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), testNamespace))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{LabelSelector: sel})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	matching := makeTenantSecret("harbor-test-credentials", map[string]string{
		"apps.cozystack.io/application.kind": "Harbor",
		"apps.cozystack.io/application.name": "test",
	})
	other := makeTenantSecret("postgres-new-credentials", map[string]string{
		"apps.cozystack.io/application.kind": "Postgres",
		"apps.cozystack.io/application.name": "new",
	})
	if err := r.c.Create(ctx, matching); err != nil {
		t.Fatalf("create matching secret: %v", err)
	}
	if err := r.c.Create(ctx, other); err != nil {
		t.Fatalf("create other secret: %v", err)
	}

	got := collectAddedNames(t, w, 500*time.Millisecond)
	want := []string{"harbor-test-credentials"}

	if !equalStrings(got, want) {
		t.Fatalf("Watch ignored labelSelector: got %v, want %v", got, want)
	}
}

// collectAddedNames drains ADDED events from a watch until the timeout fires,
// returning the sorted list of object names. Bookmarks are ignored. Used to
// assert that nothing extra leaks past a label-selector filter.
func collectAddedNames(t *testing.T, w watch.Interface, timeout time.Duration) []string {
	t.Helper()
	names := make([]string, 0)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		select {
		case ev, ok := <-w.ResultChan():
			if !ok {
				sort.Strings(names)
				return names
			}
			if ev.Type != watch.Added {
				continue
			}
			ts, ok := ev.Object.(*corev1alpha1.TenantSecret)
			if !ok {
				t.Fatalf("expected *TenantSecret in event, got %T", ev.Object)
			}
			names = append(names, ts.Name)
		case <-deadline.C:
			sort.Strings(names)
			return names
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
