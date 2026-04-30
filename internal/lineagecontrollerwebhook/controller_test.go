package lineagecontrollerwebhook

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newWebhookScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1: %v", err)
	}
	return scheme
}

// TestReconcile_EmptyClusterStoresEmptyMap pins the empty-cluster
// reconcile: with no ApplicationDefinitions present, the reconciler
// must store a fresh runtimeConfig with an empty map (not leave the
// stored config nil, not return an error).
func TestReconcile_EmptyClusterStoresEmptyMap(t *testing.T) {
	scheme := newWebhookScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	w := &LineageControllerWebhook{Client: fakeClient, Scheme: scheme}
	if _, err := w.Reconcile(context.TODO(), ctrl.Request{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, ok := w.config.Load().(*runtimeConfig)
	if !ok {
		t.Fatalf("expected *runtimeConfig stored, got %T", w.config.Load())
	}
	if cfg.appCRDMap == nil {
		t.Errorf("expected non-nil empty map")
	}
	if len(cfg.appCRDMap) != 0 {
		t.Errorf("expected 0 entries, got %d", len(cfg.appCRDMap))
	}
}

// TestReconcile_BuildsAppCRDMapByKind pins the documented mapping:
// each ApplicationDefinition is keyed by (group=apps.cozystack.io,
// kind=Spec.Application.Kind). The map values point to the
// ApplicationDefinition itself.
func TestReconcile_BuildsAppCRDMapByKind(t *testing.T) {
	scheme := newWebhookScheme(t)

	harbor := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
		},
	}
	bucket := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "bucket"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Bucket"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(harbor, bucket).
		Build()

	w := &LineageControllerWebhook{Client: fakeClient, Scheme: scheme}
	if _, err := w.Reconcile(context.TODO(), ctrl.Request{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := w.config.Load().(*runtimeConfig)
	if len(cfg.appCRDMap) != 2 {
		t.Fatalf("expected 2 entries, got %d (%+v)", len(cfg.appCRDMap), cfg.appCRDMap)
	}
	if got := cfg.appCRDMap[appRef{"apps.cozystack.io", "Harbor"}]; got == nil || got.Name != "harbor" {
		t.Errorf("expected Harbor → harbor entry, got %+v", got)
	}
	if got := cfg.appCRDMap[appRef{"apps.cozystack.io", "Bucket"}]; got == nil || got.Name != "bucket" {
		t.Errorf("expected Bucket → bucket entry, got %+v", got)
	}
}

// TestReconcile_DuplicateKindKeepsFirst pins the duplicate handling:
// when two ApplicationDefinitions have the same Application.Kind, the
// reconciler keeps the first one and logs about the duplicate. (Order
// is whatever the API returns; the test just asserts uniqueness.)
func TestReconcile_DuplicateKindKeepsFirst(t *testing.T) {
	scheme := newWebhookScheme(t)

	first := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
		},
	}
	dup := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor-shadow"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(first, dup).
		Build()

	w := &LineageControllerWebhook{Client: fakeClient, Scheme: scheme}
	if _, err := w.Reconcile(context.TODO(), ctrl.Request{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := w.config.Load().(*runtimeConfig)
	if len(cfg.appCRDMap) != 1 {
		t.Fatalf("expected exactly one Harbor entry (duplicate dropped), got %d", len(cfg.appCRDMap))
	}
}
