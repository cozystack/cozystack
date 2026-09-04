package controller

import (
	"context"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newAppGroupDefScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1: %v", err)
	}
	// The reconciler manages APIServices as unstructured objects; teach the
	// fake client the kind without pulling in kube-aggregator.
	scheme.AddKnownTypeWithName(apiServiceGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(apiServiceGVK.GroupVersion().WithKind("APIServiceList"), &unstructured.UnstructuredList{})
	return scheme
}

func referenceAPIService() *unstructured.Unstructured {
	ref := &unstructured.Unstructured{}
	ref.SetGroupVersionKind(apiServiceGVK)
	ref.SetName(referenceAPIServiceName)
	ref.SetAnnotations(map[string]string{certManagerInjectAnnotation: "cozy-system/cozystack-api"})
	_ = unstructured.SetNestedMap(ref.Object, map[string]any{
		"name":      "cozystack-api",
		"namespace": "cozy-system",
	}, "spec", "service")
	_ = unstructured.SetNestedField(ref.Object, "apps.cozystack.io", "spec", "group")
	_ = unstructured.SetNestedField(ref.Object, "v1alpha1", "spec", "version")
	return ref
}

func appGroupDef(group string) *cozyv1alpha1.ApplicationGroupDefinition {
	return &cozyv1alpha1.ApplicationGroupDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: group, UID: types.UID("uid-" + group)},
		Spec:       cozyv1alpha1.ApplicationGroupDefinitionSpec{Group: group},
	}
}

func reconcileGroupDef(t *testing.T, c client.Client, scheme *runtime.Scheme, name string) {
	t.Helper()
	r := &ApplicationGroupDefinitionReconciler{Client: c, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// A registered group gets an APIService cloned from the reference: same
// backend service, same CA-injection annotation, owned by the registration
// so deletion garbage-collects it.
func TestApplicationGroupDefinition_CreatesAPIService(t *testing.T) {
	scheme := newAppGroupDefScheme(t)
	groupDef := appGroupDef("apps.example.com")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(referenceAPIService(), groupDef).Build()

	reconcileGroupDef(t, c, scheme, "apps.example.com")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(apiServiceGVK)
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "v1alpha1.apps.example.com"}, got); err != nil {
		t.Fatalf("expected APIService to be created: %v", err)
	}
	group, _, _ := unstructured.NestedString(got.Object, "spec", "group")
	if group != "apps.example.com" {
		t.Errorf("spec.group = %q, want apps.example.com", group)
	}
	svcName, _, _ := unstructured.NestedString(got.Object, "spec", "service", "name")
	svcNS, _, _ := unstructured.NestedString(got.Object, "spec", "service", "namespace")
	if svcName != "cozystack-api" || svcNS != "cozy-system" {
		t.Errorf("spec.service = %s/%s, want cozy-system/cozystack-api", svcNS, svcName)
	}
	if got.GetAnnotations()[certManagerInjectAnnotation] != "cozy-system/cozystack-api" {
		t.Errorf("CA-injection annotation not cloned: %v", got.GetAnnotations())
	}
	refs := got.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "ApplicationGroupDefinition" || refs[0].Name != "apps.example.com" {
		t.Errorf("owner references = %+v, want the ApplicationGroupDefinition", refs)
	}
}

// Reconciling an existing APIService enforces the managed fields but must
// not clobber fields owned by others — notably the cert-manager-injected
// caBundle.
func TestApplicationGroupDefinition_UpdatePreservesCABundle(t *testing.T) {
	scheme := newAppGroupDefScheme(t)
	groupDef := appGroupDef("apps.example.com")

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(apiServiceGVK)
	existing.SetName("v1alpha1.apps.example.com")
	_ = unstructured.SetNestedField(existing.Object, "SOMECABUNDLE", "spec", "caBundle")
	_ = unstructured.SetNestedMap(existing.Object, map[string]any{
		"name":      "stale-service",
		"namespace": "stale-ns",
	}, "spec", "service")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(referenceAPIService(), groupDef, existing).Build()

	reconcileGroupDef(t, c, scheme, "apps.example.com")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(apiServiceGVK)
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "v1alpha1.apps.example.com"}, got); err != nil {
		t.Fatalf("get APIService: %v", err)
	}
	caBundle, _, _ := unstructured.NestedString(got.Object, "spec", "caBundle")
	if caBundle != "SOMECABUNDLE" {
		t.Errorf("spec.caBundle = %q, want preserved SOMECABUNDLE", caBundle)
	}
	svcName, _, _ := unstructured.NestedString(got.Object, "spec", "service", "name")
	if svcName != "cozystack-api" {
		t.Errorf("spec.service.name = %q, want repaired cozystack-api", svcName)
	}
	if len(got.GetOwnerReferences()) != 1 {
		t.Errorf("owner reference not adopted: %+v", got.GetOwnerReferences())
	}
}

// A registration whose group somehow bypassed CRD validation (reserved or
// malformed) must not materialize an APIService.
func TestApplicationGroupDefinition_InvalidGroupIgnored(t *testing.T) {
	scheme := newAppGroupDefScheme(t)
	groupDef := appGroupDef("evil.cozystack.io")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(referenceAPIService(), groupDef).Build()

	reconcileGroupDef(t, c, scheme, "evil.cozystack.io")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(apiServiceGVK)
	err := c.Get(context.TODO(), types.NamespacedName{Name: "v1alpha1.evil.cozystack.io"}, got)
	if err == nil {
		t.Fatal("APIService was created for a reserved group")
	}
}

// ApplicationGroupDefinitions are part of the cozystack-api startup config,
// so adding one must change the config hash that triggers the debounced
// restart of the deployment.
func TestConfigHash_ChangesWithGroupDefinitions(t *testing.T) {
	scheme := newAppGroupDefScheme(t)
	c1 := fake.NewClientBuilder().WithScheme(scheme).Build()
	c2 := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(appGroupDef("apps.example.com")).Build()

	r1 := &ApplicationDefinitionReconciler{Client: c1, Scheme: scheme, Debounce: time.Second}
	r2 := &ApplicationDefinitionReconciler{Client: c2, Scheme: scheme, Debounce: time.Second}

	h1, err := r1.computeConfigHash(context.TODO())
	if err != nil {
		t.Fatalf("hash without group defs: %v", err)
	}
	h2, err := r2.computeConfigHash(context.TODO())
	if err != nil {
		t.Fatalf("hash with group defs: %v", err)
	}
	if h1 == h2 {
		t.Error("config hash did not change when an ApplicationGroupDefinition was added")
	}
}
