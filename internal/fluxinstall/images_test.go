package fluxinstall

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/fluxcd/pkg/apis/kustomize"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cozystack/cozystack/internal/manifestutil"
)

const testDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

// fluxImages are the images the embedded manifests are expected to carry,
// without tags. A flux-aio update that renames one of them changes what an
// override has to name, so it must fail here rather than on a cluster.
var fluxImages = []string{
	"ghcr.io/fluxcd/source-controller",
	"ghcr.io/fluxcd/kustomize-controller",
	"ghcr.io/fluxcd/helm-controller",
	"ghcr.io/fluxcd/notification-controller",
	"ghcr.io/fluxcd/source-watcher",
}

func embeddedObjects(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	dir := t.TempDir()
	if err := WriteEmbeddedManifests(dir); err != nil {
		t.Fatal(err)
	}
	var objects []*unstructured.Unstructured
	for _, name := range []string{"fluxcd.yaml", "fluxcd-service.yaml"} {
		objs, err := manifestutil.ParseManifestFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, objs...)
	}
	return objects
}

func testDeployment(containers, initContainers []string) *unstructured.Unstructured {
	toList := func(images []string) []any {
		list := make([]any, 0, len(images))
		for i, image := range images {
			list = append(list, map[string]any{"name": string(rune('a' + i)), "image": image})
		}
		return list
	}
	spec := map[string]any{"containers": toList(containers)}
	if len(initContainers) > 0 {
		spec["initContainers"] = toList(initContainers)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "test", "namespace": "test"},
		"spec": map[string]any{
			"replicas": int64(1),
			"template": map[string]any{"spec": spec},
		},
	}}
}

// podImages returns the images of containers and then initContainers.
func podImages(t *testing.T, obj *unstructured.Unstructured) []string {
	t.Helper()
	var images []string
	for _, field := range []string{"containers", "initContainers"} {
		list, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", field)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list {
			images = append(images, c.(map[string]any)["image"].(string))
		}
	}
	return images
}

func TestApplyImageOverrides(t *testing.T) {
	tests := []struct {
		name           string
		containers     []string
		initContainers []string
		images         []kustomize.Image
		want           []string
	}{
		{
			name:       "newName keeps the tag",
			containers: []string{"ghcr.io/fluxcd/source-controller:v1.8.0"},
			images:     []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", NewName: "registry.example.org/flux/source-controller"}},
			want:       []string{"registry.example.org/flux/source-controller:v1.8.0"},
		},
		{
			name:       "newTag replaces the tag and drops the digest",
			containers: []string{"ghcr.io/fluxcd/source-controller:v1.8.0@" + testDigest},
			images:     []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", NewTag: "1.8.0-alt1"}},
			want:       []string{"ghcr.io/fluxcd/source-controller:1.8.0-alt1"},
		},
		{
			name:       "digest replaces the tag",
			containers: []string{"ghcr.io/fluxcd/source-controller:v1.8.0"},
			images:     []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", Digest: testDigest}},
			want:       []string{"ghcr.io/fluxcd/source-controller@" + testDigest},
		},
		{
			name:       "newTag with digest keeps both",
			containers: []string{"ghcr.io/fluxcd/source-controller:v1.8.0"},
			images:     []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", NewName: "registry.example.org/flux/source-controller", NewTag: "1.8.0", Digest: testDigest}},
			want:       []string{"registry.example.org/flux/source-controller:1.8.0@" + testDigest},
		},
		{
			name:       "an image without a tag matches",
			containers: []string{"ghcr.io/fluxcd/source-controller"},
			images:     []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", NewTag: "v1.8.0"}},
			want:       []string{"ghcr.io/fluxcd/source-controller:v1.8.0"},
		},
		{
			name:       "a registry port is part of the name, not a tag",
			containers: []string{"registry.local:5000/fluxcd/source-controller:v1.8.0"},
			images:     []kustomize.Image{{Name: "registry.local:5000/fluxcd/source-controller", NewTag: "v1.8.1"}},
			want:       []string{"registry.local:5000/fluxcd/source-controller:v1.8.1"},
		},
		{
			name:       "a name that is only a prefix does not match",
			containers: []string{"ghcr.io/fluxcd/source-controller-extra:v1.8.0", "ghcr.io/fluxcd/source-controller:v1.8.0"},
			images:     []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", NewTag: "v1.8.1"}},
			want:       []string{"ghcr.io/fluxcd/source-controller-extra:v1.8.0", "ghcr.io/fluxcd/source-controller:v1.8.1"},
		},
		{
			name:           "initContainers are rewritten too",
			containers:     []string{"ghcr.io/fluxcd/helm-controller:v1.5.0"},
			initContainers: []string{"ghcr.io/fluxcd/source-controller:v1.8.0"},
			images:         []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", NewTag: "v1.8.1"}},
			want:           []string{"ghcr.io/fluxcd/helm-controller:v1.5.0", "ghcr.io/fluxcd/source-controller:v1.8.1"},
		},
		{
			// kustomize runs one transformer per entry, so a later entry
			// sees what an earlier one wrote.
			name:       "entries apply in order",
			containers: []string{"ghcr.io/fluxcd/source-controller:v1.8.0"},
			images: []kustomize.Image{
				{Name: "ghcr.io/fluxcd/source-controller", NewName: "registry.example.org/flux/source-controller"},
				{Name: "registry.example.org/flux/source-controller", NewTag: "1.8.0"},
			},
			want: []string{"registry.example.org/flux/source-controller:1.8.0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := testDeployment(tt.containers, tt.initContainers)
			if err := applyImageOverrides([]*unstructured.Unstructured{obj}, tt.images); err != nil {
				t.Fatal(err)
			}
			if got := podImages(t, obj); !slices.Equal(got, tt.want) {
				t.Errorf("images = %q, want %q", got, tt.want)
			}
		})
	}
}

// The objects go on to server-side apply and are deep-copied on the way, and
// unstructured panics on a plain int, which is what a YAML round trip yields.
func TestApplyImageOverridesKeepsUnstructuredTypes(t *testing.T) {
	obj := testDeployment([]string{"ghcr.io/fluxcd/source-controller:v1.8.0"}, nil)
	images := []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", NewTag: "v1.8.1"}}
	if err := applyImageOverrides([]*unstructured.Unstructured{obj}, images); err != nil {
		t.Fatal(err)
	}
	replicas, found, err := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if err != nil || !found || replicas != 1 {
		t.Fatalf("replicas = %d (found %v, err %v), want int64 1", replicas, found, err)
	}
	obj.DeepCopy()
}

func TestApplyImageOverridesSkipsCRDs(t *testing.T) {
	crd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "examples.test.io"},
		"spec": map[string]any{
			"containers": []any{map[string]any{"image": "ghcr.io/fluxcd/source-controller:v1.8.0"}},
		},
	}}
	want := crd.DeepCopy()
	images := []kustomize.Image{{Name: "ghcr.io/fluxcd/source-controller", NewTag: "v1.8.1"}}
	if err := applyImageOverrides([]*unstructured.Unstructured{crd}, images); err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(crd.Object, want.Object) {
		t.Errorf("CRD changed: %v", crd.Object)
	}
}

func TestApplyImageOverridesEmbeddedManifests(t *testing.T) {
	t.Run("an empty list changes nothing", func(t *testing.T) {
		objects := embeddedObjects(t)
		want := make([]*unstructured.Unstructured, len(objects))
		for i, obj := range objects {
			want[i] = obj.DeepCopy()
		}
		if err := applyImageOverrides(objects, nil); err != nil {
			t.Fatal(err)
		}
		for i := range objects {
			if !apiequality.Semantic.DeepEqual(objects[i].Object, want[i].Object) {
				t.Errorf("%s %s changed", objects[i].GetKind(), objects[i].GetName())
			}
		}
	})

	t.Run("every Flux image can be overridden", func(t *testing.T) {
		objects := embeddedObjects(t)
		var images []kustomize.Image
		for _, name := range fluxImages {
			images = append(images, kustomize.Image{Name: name, NewName: "registry.example.org/" + filepath.Base(name), NewTag: "overridden"})
		}
		if err := applyImageOverrides(objects, images); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, obj := range objects {
			if obj.GetKind() == "Deployment" {
				got = append(got, podImages(t, obj)...)
			}
		}
		for _, name := range fluxImages {
			want := "registry.example.org/" + filepath.Base(name) + ":overridden"
			if !slices.Contains(got, want) {
				t.Errorf("%s was not overridden; Deployment images: %q", name, got)
			}
		}
		for _, image := range got {
			if !slices.ContainsFunc(fluxImages, func(name string) bool {
				return image == "registry.example.org/"+filepath.Base(name)+":overridden"
			}) {
				t.Errorf("image %s is not covered by the expected Flux image list", image)
			}
		}
	})
}
