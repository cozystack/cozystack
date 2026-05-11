package dashboard

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newDashboardScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1: %v", err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("add dashboard scheme: %v", err)
	}
	return scheme
}

// TestManager_Reconcile_NotFoundCleansOrphans pins the deletion path:
// when the reconciled ApplicationDefinition is missing, the manager
// kicks CleanupOrphanedResources and returns no error. The underlying
// list/delete cycle must not error when the cluster is empty.
func TestManager_Reconcile_NotFoundCleansOrphans(t *testing.T) {
	scheme := newDashboardScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	m := NewManager(fakeClient, scheme)
	res, err := m.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "missing"},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

// TestManager_Reconcile_DashboardNilEarlyReturn pins the early-return:
// an ApplicationDefinition with no Dashboard config returns from
// EnsureForAppDef immediately. We assert no error and no requeue.
func TestManager_Reconcile_DashboardNilEarlyReturn(t *testing.T) {
	scheme := newDashboardScheme(t)
	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "no-dashboard"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Quiet"},
			// Dashboard intentionally nil
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(appDef).
		Build()

	m := NewManager(fakeClient, scheme)
	res, err := m.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-dashboard"},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

// TestManager_CleanupOrphanedResources_EmptyClusterNoError pins the
// cleanup baseline: empty cluster (no ApplicationDefinitions, no
// dashboard resources) must not error when building the expected set
// or deleting orphans.
func TestManager_CleanupOrphanedResources_EmptyClusterNoError(t *testing.T) {
	scheme := newDashboardScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	m := NewManager(fakeClient, scheme)
	if err := m.CleanupOrphanedResources(context.TODO()); err != nil {
		t.Fatalf("CleanupOrphanedResources on empty cluster: %v", err)
	}
}

// TestManager_AddDashboardLabels_StaticResource pins the labelling
// helper: managed-by + resource-type are always set; CRD-derived
// labels are absent when crd is nil.
func TestManager_AddDashboardLabels_StaticResource(t *testing.T) {
	scheme := newDashboardScheme(t)
	m := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build(), scheme)

	obj := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "stub"},
	}
	m.addDashboardLabels(obj, nil, ResourceTypeStatic)

	got := obj.GetLabels()
	if got[LabelManagedBy] != ManagedByValue {
		t.Errorf("expected managed-by=%s, got %q", ManagedByValue, got[LabelManagedBy])
	}
	if got[LabelResourceType] != ResourceTypeStatic {
		t.Errorf("expected resource-type=%s, got %q", ResourceTypeStatic, got[LabelResourceType])
	}
	if _, ok := got[LabelCRDName]; ok {
		t.Errorf("expected no CRD-derived labels for static resource (crd nil), got %v", got)
	}
}

// TestManager_AddDashboardLabels_DynamicResource pins the labelling
// helper for dynamic resources: all CRD-derived labels are written.
func TestManager_AddDashboardLabels_DynamicResource(t *testing.T) {
	scheme := newDashboardScheme(t)
	m := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build(), scheme)

	crd := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{
				Kind:     "Harbor",
				Plural:   "harbors",
				Singular: "harbor",
			},
		},
	}
	obj := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "decorated"},
	}
	m.addDashboardLabels(obj, crd, ResourceTypeDynamic)

	got := obj.GetLabels()
	if got[LabelManagedBy] != ManagedByValue {
		t.Errorf("expected managed-by=%s, got %q", ManagedByValue, got[LabelManagedBy])
	}
	if got[LabelResourceType] != ResourceTypeDynamic {
		t.Errorf("expected resource-type=%s, got %q", ResourceTypeDynamic, got[LabelResourceType])
	}
	if got[LabelCRDName] != "harbor" {
		t.Errorf("expected crd-name=harbor, got %q", got[LabelCRDName])
	}
	if got[LabelCRDKind] != "Harbor" {
		t.Errorf("expected crd-kind=Harbor, got %q", got[LabelCRDKind])
	}
	if got[LabelCRDPlural] != "harbors" {
		t.Errorf("expected crd-plural=harbors, got %q", got[LabelCRDPlural])
	}
}

// TestManager_ResourceSelectors pins the three label-selector helpers
// the manager exposes for listing its own resources.
func TestManager_ResourceSelectors(t *testing.T) {
	scheme := newDashboardScheme(t)
	m := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build(), scheme)

	all := m.getDashboardResourceSelector()
	if all[LabelManagedBy] != ManagedByValue {
		t.Errorf("getDashboardResourceSelector missing managed-by: %v", all)
	}
	if _, ok := all[LabelResourceType]; ok {
		t.Errorf("getDashboardResourceSelector should not pin resource-type, got %v", all)
	}

	dyn := m.getDynamicResourceSelector()
	if dyn[LabelManagedBy] != ManagedByValue || dyn[LabelResourceType] != ResourceTypeDynamic {
		t.Errorf("getDynamicResourceSelector unexpected: %v", dyn)
	}

	stat := m.getStaticResourceSelector()
	if stat[LabelManagedBy] != ManagedByValue || stat[LabelResourceType] != ResourceTypeStatic {
		t.Errorf("getStaticResourceSelector unexpected: %v", stat)
	}
}
