package controller

import (
	"context"
	"errors"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/imageoverrides"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var overrideImage = kustomize.Image{Name: "goharbor/harbor-core", NewName: "registry.example.org/os/harbor-core"}

func overridesAppDef() *cozyv1alpha1.ApplicationDefinition {
	return &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				ChartRef: &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: "harbor-app", Namespace: "cozy-system"},
			},
		},
	}
}

func overridesRelease(postRenderers []helmv2.PostRenderer) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "harbor-release",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "Harbor",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			ChartRef:      &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: "harbor-app", Namespace: "cozy-system"},
			ValuesFrom:    expectedValuesFrom(),
			PostRenderers: postRenderers,
		},
	}
}

func reconcileWithOverrides(t *testing.T, hr *helmv2.HelmRelease, load func(context.Context) ([]kustomize.Image, bool, error)) (*helmv2.HelmRelease, error) {
	t.Helper()
	scheme := newAppDefHelmScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(overridesAppDef(), hr).Build()
	r := &ApplicationDefinitionHelmReconciler{Client: c, Scheme: scheme, ImageOverrides: load}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "harbor"}})
	got := &helmv2.HelmRelease{}
	if getErr := c.Get(context.Background(), client.ObjectKeyFromObject(hr), got); getErr != nil {
		t.Fatalf("get HelmRelease: %v", getErr)
	}
	return got, err
}

func staticOverrides(images ...kustomize.Image) func(context.Context) ([]kustomize.Image, bool, error) {
	return func(context.Context) ([]kustomize.Image, bool, error) { return images, true, nil }
}

// A release written before the table changed is brought in line, and the
// post-renderers a parent chart gave it are kept ahead of the override.
func TestAppDefHelm_SyncsImageOverrides(t *testing.T) {
	own := helmv2.PostRenderer{Kustomize: &helmv2.Kustomize{Patches: []kustomize.Patch{{Patch: "own"}}}}
	stale := imageoverrides.PostRenderers([]kustomize.Image{{Name: "goharbor/harbor-core", NewName: "stale"}})[0]

	got, err := reconcileWithOverrides(t, overridesRelease([]helmv2.PostRenderer{own, stale}), staticOverrides(overrideImage))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	prs := got.Spec.PostRenderers
	if len(prs) != 2 || prs[0].Kustomize.Patches[0].Patch != "own" || len(prs[1].Kustomize.Images) != 1 || prs[1].Kustomize.Images[0] != overrideImage {
		t.Fatalf("want [own, override], got %+v", prs)
	}
}

func TestAppDefHelm_DropsImageOverridesWhenTableEmptied(t *testing.T) {
	stale := imageoverrides.PostRenderers([]kustomize.Image{overrideImage})
	got, err := reconcileWithOverrides(t, overridesRelease(stale), staticOverrides())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.Spec.PostRenderers != nil {
		t.Fatalf("want no post-renderers, got %+v", got.Spec.PostRenderers)
	}
}

// Without a loader the reconciler does not own the field at all.
func TestAppDefHelm_LeavesPostRenderersWithoutLoader(t *testing.T) {
	existing := imageoverrides.PostRenderers([]kustomize.Image{overrideImage})
	got, err := reconcileWithOverrides(t, overridesRelease(existing), nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(got.Spec.PostRenderers) != 1 {
		t.Fatalf("want the post-renderer untouched, got %+v", got.Spec.PostRenderers)
	}
}

// A table that cannot be read must not strip the overrides releases carry.
func TestAppDefHelm_KeepsImageOverridesOnLoadError(t *testing.T) {
	existing := imageoverrides.PostRenderers([]kustomize.Image{overrideImage})
	got, err := reconcileWithOverrides(t, overridesRelease(existing), func(context.Context) ([]kustomize.Image, bool, error) {
		return nil, true, errors.New("broken table")
	})
	if err == nil {
		t.Fatal("want a reconcile error")
	}
	if len(got.Spec.PostRenderers) != 1 {
		t.Fatalf("want the post-renderer kept, got %+v", got.Spec.PostRenderers)
	}
}

// A deleted values Secret says nothing about the table, so releases keep the
// overrides they carry rather than fall back to upstream images.
func TestAppDefHelm_KeepsImageOverridesWithoutSecret(t *testing.T) {
	existing := imageoverrides.PostRenderers([]kustomize.Image{overrideImage})
	got, err := reconcileWithOverrides(t, overridesRelease(existing), func(context.Context) ([]kustomize.Image, bool, error) {
		return nil, false, nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(got.Spec.PostRenderers) != 1 || got.Spec.PostRenderers[0].Kustomize.Images[0] != overrideImage {
		t.Fatalf("want the post-renderer kept, got %+v", got.Spec.PostRenderers)
	}
}

func TestAppDefHelm_OverridesSecretEnqueuesEveryDefinition(t *testing.T) {
	scheme := newAppDefHelmScheme(t)
	other := overridesAppDef()
	other.Name = "postgres"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(overridesAppDef(), other).Build()
	r := &ApplicationDefinitionHelmReconciler{Client: c, Scheme: scheme}

	values := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: imageoverrides.DefaultSecret.Namespace, Name: imageoverrides.DefaultSecret.Name}}
	if reqs := r.allDefinitionsOnOverridesChange(context.Background(), values); len(reqs) != 2 {
		t.Fatalf("want both definitions enqueued, got %+v", reqs)
	}
	unrelated := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-foo", Name: "cozystack-values"}}
	if reqs := r.allDefinitionsOnOverridesChange(context.Background(), unrelated); len(reqs) != 0 {
		t.Fatalf("a tenant's copy must not fan out, got %+v", reqs)
	}
}
