package lineagecontrollerwebhook

import (
	"context"
	"testing"
	"time"

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

// TestReconcile_DuplicateKindKeepsOwner pins the duplicate handling: when two
// ApplicationDefinitions declare the same Application.Kind, the lineage map
// keeps the owner under the shared rule (created first, then by name), the same
// definition cozystack-api and the chartRef reconciler treat as the owner. The
// newcomer is tried on both sides of the owner by name, so neither a first-wins
// nor a last-wins walk of the list can pass.
func TestReconcile_DuplicateKindKeepsOwner(t *testing.T) {
	scheme := newWebhookScheme(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, newcomerName := range []string{"a-harbor-shadow", "z-harbor-shadow"} {
		t.Run(newcomerName, func(t *testing.T) {
			owner := &cozyv1alpha1.ApplicationDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "harbor", CreationTimestamp: metav1.NewTime(base)},
				Spec: cozyv1alpha1.ApplicationDefinitionSpec{
					Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
				},
			}
			newcomer := &cozyv1alpha1.ApplicationDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: newcomerName, CreationTimestamp: metav1.NewTime(base.Add(time.Hour))},
				Spec: cozyv1alpha1.ApplicationDefinitionSpec{
					Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
				},
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner, newcomer).Build()

			w := &LineageControllerWebhook{Client: fakeClient, Scheme: scheme}
			if _, err := w.Reconcile(context.TODO(), ctrl.Request{}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			cfg := w.config.Load().(*runtimeConfig)
			if len(cfg.appCRDMap) != 1 {
				t.Fatalf("expected exactly one Harbor entry (duplicate dropped), got %d", len(cfg.appCRDMap))
			}
			if got := cfg.appCRDMap[appRef{"apps.cozystack.io", "Harbor"}]; got == nil || got.Name != "harbor" {
				t.Fatalf("expected the owner harbor, got %+v", got)
			}
		})
	}
}
