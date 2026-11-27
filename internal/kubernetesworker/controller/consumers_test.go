package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cozystack/cozystack/internal/marketplace/naming"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	packages "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	watcherv1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func schedulerProvenance() (*helmv2.HelmRelease, []client.Object) {
	controller := true
	pkg := &packages.Package{ObjectMeta: metav1.ObjectMeta{Name: "cozystack.cozystack-scheduler", UID: "pkg-uid"}}
	ps := &packages.PackageSource{ObjectMeta: metav1.ObjectMeta{Name: pkg.Name, UID: "source-uid"}, Spec: packages.PackageSourceSpec{
		SourceRef: &packages.PackageSourceRef{Kind: "OCIRepository", Name: "cozystack-packages", Namespace: "cozy-system", Path: "/"},
		Variants:  []packages.Variant{{Name: "default", Components: []packages.Component{{Name: "cozystack-scheduler", Path: "system/cozystack-scheduler", Install: &packages.ComponentInstall{Namespace: "kube-system", ReleaseName: "cozystack-scheduler"}}}}},
	}}
	ag := &watcherv1.ArtifactGenerator{ObjectMeta: metav1.ObjectMeta{Name: ps.Name, Namespace: "cozy-system", UID: "generator-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "cozystack.io/v1alpha1", Kind: "PackageSource", Name: ps.Name, UID: ps.UID, Controller: &controller}}}, Spec: watcherv1.ArtifactGeneratorSpec{
		Sources:         []watcherv1.SourceReference{{Alias: "cozystack-packages", Kind: "OCIRepository", Name: "cozystack-packages", Namespace: "cozy-system"}},
		OutputArtifacts: []watcherv1.OutputArtifact{{Name: "cozystack-cozystack-scheduler-default-cozystack-scheduler", Copy: []watcherv1.CopyOperation{{From: "@cozystack-packages/system/cozystack-scheduler/**", To: "@artifact/cozystack-scheduler/"}}}},
	}}
	ea := &sourcev1.ExternalArtifact{ObjectMeta: metav1.ObjectMeta{Name: ag.Spec.OutputArtifacts[0].Name, Namespace: ag.Namespace, UID: "artifact-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "source.extensions.fluxcd.io/v1beta1", Kind: "ArtifactGenerator", Name: ag.Name, UID: ag.UID, Controller: &controller}}}}
	hr := pool("kube-system", "cozystack-scheduler", "")
	hr.OwnerReferences = []metav1.OwnerReference{{APIVersion: "cozystack.io/v1alpha1", Kind: "Package", Name: pkg.Name, UID: pkg.UID, Controller: &controller}}
	hr.Spec.ChartRef = &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: ea.Name, Namespace: ea.Namespace}
	hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "cozystack-values"}}
	return hr, []client.Object{pkg, ps, ag, ea}
}

func platformProvenance(source, kind, variant string) (*helmv2.HelmRelease, []client.Object) {
	hr, objects := schedulerProvenance()
	pkg := objects[0].(*packages.Package)
	ps := objects[1].(*packages.PackageSource)
	ag := objects[2].(*watcherv1.ArtifactGenerator)
	ea := objects[3].(*sourcev1.ExternalArtifact)
	pkg.Name, ps.Name = "cozystack."+source, "cozystack."+source
	pkg.Spec.Variant = variant
	ps.Spec.SourceRef.Name, ps.Spec.SourceRef.Kind = source, kind
	valuesFiles := []string{"values.yaml"}
	if variant != "default" {
		valuesFiles = append(valuesFiles, "values-"+variant+".yaml")
	}
	ps.Spec.Variants = []packages.Variant{{Name: variant, Components: []packages.Component{{Name: "platform", Path: "core/platform", Install: &packages.ComponentInstall{Namespace: "cozy-system", ReleaseName: "cozystack-platform"}, ValuesFiles: valuesFiles}}}}
	ag.Name, ag.OwnerReferences[0].Name = ps.Name, ps.Name
	ag.Spec.Sources = []watcherv1.SourceReference{{Alias: source, Kind: kind, Name: source, Namespace: "cozy-system"}}
	copies := []watcherv1.CopyOperation{{From: "@" + source + "/core/platform/**", To: "@artifact/platform/"}}
	for i, file := range valuesFiles {
		strategy := "Merge"
		if i == 0 {
			strategy = "Overwrite"
		}
		copies = append(copies, watcherv1.CopyOperation{From: "@" + source + "/core/platform/" + file, To: "@artifact/platform/values.yaml", Strategy: strategy})
	}
	ag.Spec.OutputArtifacts = []watcherv1.OutputArtifact{{Name: naming.ArtifactName(ps.Name, variant, "platform"), Copy: copies}}
	ea.Name, ea.OwnerReferences[0].Name = ag.Spec.OutputArtifacts[0].Name, ag.Name
	hr.Name, hr.Namespace = "cozystack-platform", "cozy-system"
	hr.OwnerReferences[0].Name = pkg.Name
	hr.Spec.ChartRef.Name = ea.Name
	return hr, objects
}

// A pool whose ExternalArtifact exists is traced through its whole
// provenance, and the class it selects stays only when that trace ends at the
// pool chart.
func TestPoolTracedThroughItsProvenanceRetainsItsClass(t *testing.T) {
	controller := true
	pkg := &packages.Package{ObjectMeta: metav1.ObjectMeta{Name: "cozystack.kubernetes-nodes-application", UID: "pkg-uid"}, Spec: packages.PackageSpec{Variant: "kubevirt"}}
	ps := &packages.PackageSource{ObjectMeta: metav1.ObjectMeta{Name: pkg.Name, UID: "source-uid"}, Spec: packages.PackageSourceSpec{
		SourceRef: &packages.PackageSourceRef{Kind: "OCIRepository", Name: "cozystack-packages", Namespace: "cozy-system", Path: "/"},
		Variants: []packages.Variant{{
			Name:       "kubevirt",
			Libraries:  []packages.Library{{Name: "cozy-lib", Path: "library/cozy-lib"}},
			Components: []packages.Component{{Name: "kubernetes-nodes", Path: "apps/kubernetes-nodes", Libraries: []string{"cozy-lib"}}},
		}},
	}}
	ag := &watcherv1.ArtifactGenerator{ObjectMeta: metav1.ObjectMeta{Name: ps.Name, Namespace: "cozy-system", UID: "generator-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "cozystack.io/v1alpha1", Kind: "PackageSource", Name: ps.Name, UID: ps.UID, Controller: &controller}}}, Spec: watcherv1.ArtifactGeneratorSpec{
		Sources: []watcherv1.SourceReference{{Alias: "cozystack-packages", Kind: "OCIRepository", Name: "cozystack-packages", Namespace: "cozy-system"}},
		OutputArtifacts: []watcherv1.OutputArtifact{{Name: naming.ArtifactName(ps.Name, "kubevirt", "kubernetes-nodes"), Copy: []watcherv1.CopyOperation{
			{From: "@cozystack-packages/apps/kubernetes-nodes/**", To: "@artifact/kubernetes-nodes/"},
			{From: "@cozystack-packages/library/cozy-lib/**", To: "@artifact/kubernetes-nodes/charts/cozy-lib/"},
		}}},
	}}
	ea := &sourcev1.ExternalArtifact{ObjectMeta: metav1.ObjectMeta{Name: ag.Spec.OutputArtifacts[0].Name, Namespace: ag.Namespace, UID: "artifact-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "source.extensions.fluxcd.io/v1beta1", Kind: "ArtifactGenerator", Name: ag.Name, UID: ag.UID, Controller: &controller}}}}
	hr := pool("tenant-test", "kubernetes-nodes-k-md0", "custom")
	if hr.Spec.ChartRef.Name != ea.Name {
		t.Fatalf("pool chart artifact %q is not the traced one %q", hr.Spec.ChartRef.Name, ea.Name)
	}
	r, c := newReconcilerWith(t, interceptor.Funcs{}, pkg, ps, ag, ea, hr, terminating(class("custom", Finalizer)))
	reconcileClass(t, r, "custom")
	if got := finalizers(t, c, "custom"); len(got) != 1 || got[0] != Finalizer {
		t.Fatalf("traced pool did not retain its class: finalizers %v", got)
	}
}

func TestPlatformRootReleasesUnusedClassWithoutReadingValues(t *testing.T) {
	for _, source := range []string{"cozystack-platform", "custom-platform"} {
		for _, kind := range []string{"OCIRepository", "GitRepository"} {
			for _, variant := range []string{"default", "isp-full"} {
				t.Run(strings.Join([]string{source, kind, variant}, "/"), func(t *testing.T) {
					hr, objects := platformProvenance(source, kind, variant)
					objects = append(objects, hr, terminating(class("unused", Finalizer)))
					r, c := newReconcilerWith(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*corev1.Secret); ok {
							t.Fatal("platform root Secret GET")
						}
						return c.Get(ctx, key, obj, opts...)
					}}, objects...)
					reconcileClass(t, r, "unused")
					if err := c.Get(context.Background(), client.ObjectKey{Name: "unused"}, class("unused")); !apierrors.IsNotFound(err) {
						t.Fatalf("root kept unused class: %v", err)
					}
				})
			}
		}
	}
}

func TestContradictoryPlatformRootRetainsClass(t *testing.T) {
	for _, mutation := range []string{"no owner", "owner UID", "source namespace", "source name", "component path", "copy", "release namespace", "release name"} {
		t.Run(mutation, func(t *testing.T) {
			hr, objects := platformProvenance("cozystack-platform", "OCIRepository", "default")
			switch mutation {
			case "no owner":
				hr.OwnerReferences = nil
			case "owner UID":
				hr.OwnerReferences[0].UID = "stale"
			case "source namespace":
				objects[1].(*packages.PackageSource).Spec.SourceRef.Namespace = "other"
			case "source name":
				objects[1].(*packages.PackageSource).Spec.SourceRef.Name = "other"
			case "component path":
				objects[1].(*packages.PackageSource).Spec.Variants[0].Components[0].Path = "apps/kubernetes-nodes"
			case "copy":
				objects[2].(*watcherv1.ArtifactGenerator).Spec.OutputArtifacts[0].Copy[0].From = "@cozystack-platform/apps/kubernetes-nodes/**"
			case "release namespace":
				hr.Namespace = "other"
			case "release name":
				hr.Name = "other"
			}
			objects = append(objects, hr)
			r, _ := newReconcilerWith(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					t.Fatal("ambiguous root read Secret")
				}
				return c.Get(ctx, key, obj, opts...)
			}}, objects...)
			if inUse, err := r.referenced(context.Background(), "unused"); !inUse && err == nil {
				t.Fatal("ambiguous root silently released class")
			}
		})
	}
}

func TestKnownSchedulerNeverReadsValuesOrRetainsClass(t *testing.T) {
	hr, objects := schedulerProvenance()
	objects = append(objects, hr, terminating(class("acme", Finalizer)))
	gets := 0
	r, _ := newReconcilerWith(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Secret); ok {
			gets++
			t.Error("scheduler Secret GET")
		}
		return c.Get(ctx, key, obj, opts...)
	}}, objects...)
	inUse, err := r.referenced(context.Background(), "acme")
	if err != nil || inUse || gets != 0 {
		t.Fatalf("inUse=%v err=%v gets=%d", inUse, err, gets)
	}
}

func TestKnownApplicationWithoutPackageOwnerSkipsValues(t *testing.T) {
	hr, objects := schedulerProvenance()
	pkg := objects[0].(*packages.Package)
	ps := objects[1].(*packages.PackageSource)
	ag := objects[2].(*watcherv1.ArtifactGenerator)
	ea := objects[3].(*sourcev1.ExternalArtifact)
	pkg.Name, ps.Name = "cozystack.postgres-application", "cozystack.postgres-application"
	ps.Spec.Variants[0].Libraries = []packages.Library{{Name: "cozy-lib", Path: "library/cozy-lib"}}
	ps.Spec.Variants[0].Components = []packages.Component{{Name: "postgres", Path: "apps/postgres", Libraries: []string{"cozy-lib"}}}
	ag.Name, ag.OwnerReferences[0].Name = ps.Name, ps.Name
	ag.Spec.OutputArtifacts = []watcherv1.OutputArtifact{{Name: "cozystack-postgres-application-default-postgres", Copy: []watcherv1.CopyOperation{
		{From: "@cozystack-packages/apps/postgres/**", To: "@artifact/postgres/"},
		{From: "@cozystack-packages/library/cozy-lib/**", To: "@artifact/postgres/charts/cozy-lib/"},
	}}}
	ea.Name, ea.OwnerReferences[0].Name = ag.Spec.OutputArtifacts[0].Name, ag.Name
	hr.Namespace, hr.Name, hr.OwnerReferences = "tenant-root", "postgres-example", nil
	hr.Spec.ChartRef.Name = ea.Name
	hr.Spec.Values.Raw = []byte(`{"workerClassName":"not-a-worker-setting"}`)
	objects = append(objects, hr)
	r, _ := newReconcilerWith(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Secret); ok {
			t.Fatal("unrelated application Secret GET")
		}
		return c.Get(ctx, key, obj, opts...)
	}}, objects...)
	if inUse, err := r.referenced(context.Background(), "not-a-worker-setting"); err != nil || inUse {
		t.Fatalf("inUse=%v err=%v", inUse, err)
	}
}

func TestAmbiguousConsumerRetainsWithoutReadingValues(t *testing.T) {
	for _, mutation := range []string{"owner UID", "source", "copy", "unlabelled", "forged label", "canonical contradiction", "canonical artifact contradiction", "artifact owner UID", "source owner UID", "missing package", "malformed provenance", "path alias", "worker chart as library", "duplicate artifact"} {
		t.Run(mutation, func(t *testing.T) {
			hr, objects := schedulerProvenance()
			switch mutation {
			case "artifact owner UID":
				objects[3].(*sourcev1.ExternalArtifact).OwnerReferences[0].UID = "stale"
			case "source owner UID":
				objects[2].(*watcherv1.ArtifactGenerator).OwnerReferences[0].UID = "stale"
			case "missing package":
				objects = objects[1:]
			case "malformed provenance":
				objects[2].(*watcherv1.ArtifactGenerator).Spec.Sources = nil
			case "duplicate artifact":
				ag := objects[2].(*watcherv1.ArtifactGenerator)
				ag.Spec.OutputArtifacts = append(ag.Spec.OutputArtifacts, watcherv1.OutputArtifact{Name: ag.Spec.OutputArtifacts[0].Name, Copy: []watcherv1.CopyOperation{{From: "@cozystack-packages/apps/kubernetes-nodes/**", To: "@artifact/kubernetes-nodes/"}}})
			case "worker chart as library":
				ps := objects[1].(*packages.PackageSource)
				ps.Spec.Variants[0].Components[0].Libraries = []string{"workers"}
				ps.Spec.Variants[0].Libraries = []packages.Library{{Name: "workers", Path: "apps/kubernetes-nodes"}}
				ag := objects[2].(*watcherv1.ArtifactGenerator)
				ag.Spec.OutputArtifacts[0].Copy = append(ag.Spec.OutputArtifacts[0].Copy, watcherv1.CopyOperation{From: "@cozystack-packages/apps/kubernetes-nodes/**", To: "@artifact/cozystack-scheduler/charts/workers/"})
			case "path alias":
				objects[1].(*packages.PackageSource).Spec.Variants[0].Components[0].Path = "apps/other/../kubernetes-nodes"
				objects[2].(*watcherv1.ArtifactGenerator).Spec.OutputArtifacts[0].Copy = []watcherv1.CopyOperation{{From: "@cozystack-packages/apps/other/../kubernetes-nodes/**", To: "@artifact/kubernetes-nodes/"}}
			case "owner UID":
				hr.OwnerReferences[0].UID = "stale"
			case "source":
				objects[1].(*packages.PackageSource).Spec.SourceRef.Name = "unknown"
			case "copy":
				objects[2].(*watcherv1.ArtifactGenerator).Spec.OutputArtifacts[0].Copy[0].From = "@cozystack-packages/apps/kubernetes-nodes/**"
			case "unlabelled":
				hr.OwnerReferences = nil
				hr.Spec.ChartRef.Name = "custom"
			case "forged label":
				hr.OwnerReferences = nil
				hr.Spec.ChartRef.Name = "custom"
				hr.Labels = map[string]string{"cozystack.io/package": "cozystack.cozystack-scheduler"}
			case "canonical contradiction":
				hr.Spec.ChartRef.Name = "cozystack-kubernetes-nodes-application-kubevirt-kubernetes-nodes"
			case "canonical artifact contradiction":
				hr.OwnerReferences = nil
				hr.Spec.ChartRef.Name = "cozystack-kubernetes-nodes-application-kubevirt-kubernetes-nodes"
				objects[3].SetName(hr.Spec.ChartRef.Name)
			}
			objects = append(objects, hr)
			r, _ := newReconcilerWith(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					t.Fatal("ambiguous consumer read Secret")
				}
				return c.Get(ctx, key, obj, opts...)
			}}, objects...)
			inUse, err := r.referenced(context.Background(), "acme")
			if !inUse && err == nil {
				t.Fatal("ambiguous consumer silently released class")
			}
		})
	}
}

func TestCanonicalPoolArtifactsAreTheOnesThePlatformInstallsFrom(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for file, name := range map[string]string{
		"packages/system/kubernetes-nodes-rd/cozyrds/kubernetes-nodes.yaml": applicationPoolArtifact,
		"packages/extra/computeplane/templates/cluster.yaml":                computeplanePoolArtifact,
	} {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "name: "+name+"\n") {
			t.Fatalf("%s names no chart artifact %q", file, name)
		}
	}
}
