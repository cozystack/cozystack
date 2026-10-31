package operator

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/imageoverrides"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func overridesPS() *cozyv1alpha1.PackageSource {
	return &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "demo.app"},
		Spec: cozyv1alpha1.PackageSourceSpec{Variants: []cozyv1alpha1.Variant{{
			Name: "default",
			Components: []cozyv1alpha1.Component{{Name: "op", Path: "sys/op",
				Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-op"}}},
		}}},
	}
}

func overridesSecret(values string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: imageoverrides.DefaultSecret.Namespace, Name: imageoverrides.DefaultSecret.Name},
		Data:       map[string][]byte{"values.yaml": []byte(values)},
	}
}

func reconcileOverrides(t *testing.T, objs ...client.Object) (client.Client, error) {
	t.Helper()
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "demo.app"}, Spec: cozyv1alpha1.PackageSpec{Variant: "default"}}
	objs = append(objs, pkg, overridesPS())
	cl := fake.NewClientBuilder().WithScheme(privScheme()).WithObjects(objs...).WithStatusSubresource(&cozyv1alpha1.Package{}).Build()
	r := &PackageReconciler{Client: cl, Scheme: privScheme()}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo.app"}})
	return cl, err
}

func getOverridesHR(t *testing.T, cl client.Client) *helmv2.HelmRelease {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "cozy-op", Name: "op"}, hr); err != nil {
		t.Fatalf("get HelmRelease: %v", err)
	}
	return hr
}

func TestPackageReconcilerAppliesImageOverrides(t *testing.T) {
	cl, err := reconcileOverrides(t, overridesSecret(`
_cluster:
  image-overrides:
  - name: ghcr.io/cozystack/cozystack/kubeovn
    newName: registry.example.org/os/cozystack-kube-ovn
    newTag: "1.15.26"
`))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	hr := getOverridesHR(t, cl)
	if len(hr.Spec.PostRenderers) != 1 || hr.Spec.PostRenderers[0].Kustomize == nil {
		t.Fatalf("want one kustomize post-renderer, got %+v", hr.Spec.PostRenderers)
	}
	images := hr.Spec.PostRenderers[0].Kustomize.Images
	if len(images) != 1 || images[0].Name != "ghcr.io/cozystack/cozystack/kubeovn" ||
		images[0].NewName != "registry.example.org/os/cozystack-kube-ovn" || images[0].NewTag != "1.15.26" {
		t.Fatalf("unexpected images: %+v", images)
	}
}

func TestPackageReconcilerWithoutImageOverrides(t *testing.T) {
	for name, objs := range map[string][]client.Object{
		"no secret":   nil,
		"empty table": {overridesSecret("_cluster:\n  root-host: example.org\n")},
	} {
		t.Run(name, func(t *testing.T) {
			cl, err := reconcileOverrides(t, objs...)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if hr := getOverridesHR(t, cl); hr.Spec.PostRenderers != nil {
				t.Fatalf("want no post-renderers, got %+v", hr.Spec.PostRenderers)
			}
		})
	}
}

// Installing without the overrides would pull images an air-gapped cluster
// cannot reach, so a broken table stops the Package and says why.
func TestPackageReconcilerRefusesInvalidImageOverrides(t *testing.T) {
	cl, err := reconcileOverrides(t, overridesSecret("_cluster:\n  image-overrides:\n  - name: a/b\n"))
	if err == nil {
		t.Fatal("want a reconcile error for an invalid table")
	}
	var hrs helmv2.HelmReleaseList
	if err := cl.List(context.Background(), &hrs); err != nil {
		t.Fatal(err)
	}
	if len(hrs.Items) != 0 {
		t.Fatalf("want no HelmRelease, got %d", len(hrs.Items))
	}
	var pkg cozyv1alpha1.Package
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "demo.app"}, &pkg); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range pkg.Status.Conditions {
		if c.Type == "Ready" && c.Reason == "ImageOverridesInvalid" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want Ready=False ImageOverridesInvalid, got %+v", pkg.Status.Conditions)
	}
}

// Removing the table must take the post-renderer off a release that already has one.
func TestPackageReconcilerDropsRemovedImageOverrides(t *testing.T) {
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "op", Namespace: "cozy-op"},
		Spec:       helmv2.HelmReleaseSpec{PostRenderers: imageoverrides.PostRenderers([]kustomize.Image{{Name: "a/b", NewName: "c/d"}})},
	}
	cl, err := reconcileOverrides(t, existing, overridesSecret("_cluster: {}\n"))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if hr := getOverridesHR(t, cl); hr.Spec.PostRenderers != nil {
		t.Fatalf("want the post-renderer removed, got %+v", hr.Spec.PostRenderers)
	}
}

// A deleted values Secret says nothing about the table, so the releases keep
// the overrides they carry rather than fall back to upstream images.
func TestPackageReconcilerKeepsImageOverridesWithoutSecret(t *testing.T) {
	want := imageoverrides.PostRenderers([]kustomize.Image{{Name: "a/b", NewName: "c/d"}})
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "op", Namespace: "cozy-op"},
		Spec:       helmv2.HelmReleaseSpec{PostRenderers: want},
	}
	cl, err := reconcileOverrides(t, existing)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if hr := getOverridesHR(t, cl); !apiequality.Semantic.DeepEqual(hr.Spec.PostRenderers, want) {
		t.Fatalf("want the post-renderer kept, got %+v", hr.Spec.PostRenderers)
	}
}
