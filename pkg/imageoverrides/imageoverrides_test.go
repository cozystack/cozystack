package imageoverrides

import (
	"context"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const digest = "sha256:f80b60d4523dcdecb225c3113bcbd0eedc45b4d2b46f87c0ccf4f14d09a4a35f"

func TestParse(t *testing.T) {
	values := `
keda:
  clusterDomain: cozy.local
_cluster:
  root-host: example.org
  image-overrides:
  - name: ghcr.io/cozystack/cozystack/kubeovn
    newName: registry.example.org/os/cozystack-kube-ovn
    newTag: "1.15.26"
    digest: ` + digest + `
  - name: registry.k8s.io/sig-storage/csi-attacher
    newName: registry.example.org/os/external-attacher
`
	got, err := Parse([]byte(values))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []kustomize.Image{
		{Name: "ghcr.io/cozystack/cozystack/kubeovn", NewName: "registry.example.org/os/cozystack-kube-ovn", NewTag: "1.15.26", Digest: digest},
		{Name: "registry.k8s.io/sig-storage/csi-attacher", NewName: "registry.example.org/os/external-attacher"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d overrides, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("override %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseWithoutTable(t *testing.T) {
	for name, values := range map[string]string{
		"no _cluster":   "keda: {clusterDomain: cozy.local}\n",
		"no key":        "_cluster:\n  root-host: example.org\n",
		"null table":    "_cluster:\n  image-overrides: null\n",
		"empty payload": "",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse([]byte(values))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("got %+v, want no overrides", got)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		table string
		want  string
	}{
		"unknown field":     {"- {name: a/b, newImage: c/d}", "unknown field"},
		"missing name":      {"- {newName: c/d}", "name is required"},
		"name with tag":     {"- {name: 'a/b:v1', newName: c/d}", "must not carry a tag"},
		"name with digest":  {"- {name: 'a/b@" + digest + "', newName: c/d}", "must not carry a tag"},
		"nothing to change": {"- {name: a/b}", "sets none of"},
		"malformed digest":  {"- {name: a/b, digest: 'sha256:abc'}", "is not sha256"},
		"duplicate name":    {"- {name: a/b, newName: c/d}\n- {name: a/b, newTag: v2}", "listed twice"},
		"not a list":        {"{name: a/b}", "parsing image-overrides"},
	} {
		t.Run(name, func(t *testing.T) {
			table := "    " + strings.ReplaceAll(tc.table, "\n", "\n    ")
			_, err := Parse([]byte("_cluster:\n  image-overrides:\n" + table + "\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got error %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// A registry host with a port must not be mistaken for a tag.
func TestValidateAcceptsRegistryPort(t *testing.T) {
	err := Validate([]kustomize.Image{{Name: "registry.example.org:5000/os/kubeovn", NewTag: "v1"}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestPostRenderers(t *testing.T) {
	if got := PostRenderers(nil); got != nil {
		t.Fatalf("empty table: got %+v, want nil", got)
	}
	images := []kustomize.Image{{Name: "a/b", NewName: "c/d"}}
	got := PostRenderers(images)
	if len(got) != 1 || got[0].Kustomize == nil || len(got[0].Kustomize.Images) != 1 || got[0].Kustomize.Images[0] != images[0] {
		t.Fatalf("got %+v", got)
	}
	if len(got[0].Kustomize.Patches) != 0 {
		t.Fatalf("unexpected patches: %+v", got[0].Kustomize.Patches)
	}
	images[0].NewName = "changed"
	if got[0].Kustomize.Images[0].NewName != "c/d" {
		t.Fatal("PostRenderers shares the caller's slice")
	}
}

func TestLoad(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	t.Run("missing secret", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		got, found, err := Load(context.Background(), c, DefaultSecret)
		if err != nil || found || got != nil {
			t.Fatalf("got %+v, found=%v, %v; want nothing found and no error", got, found, err)
		}
	})

	t.Run("secret with table", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: DefaultSecret.Namespace, Name: DefaultSecret.Name},
			Data: map[string][]byte{"values.yaml": []byte(
				"_cluster:\n  image-overrides:\n  - {name: a/b, newName: c/d}\n")},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
		got, found, err := Load(context.Background(), c, DefaultSecret)
		if err != nil || !found || len(got) != 1 || got[0].NewName != "c/d" {
			t.Fatalf("got %+v, found=%v, %v", got, found, err)
		}
	})

	t.Run("secret without table", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: DefaultSecret.Namespace, Name: DefaultSecret.Name},
			Data:       map[string][]byte{"values.yaml": []byte("_cluster: {}\n")},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
		got, found, err := Load(context.Background(), c, DefaultSecret)
		if err != nil || !found || len(got) != 0 {
			t.Fatalf("got %+v, found=%v, %v; want an empty table that was found", got, found, err)
		}
	})

	t.Run("invalid table", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: DefaultSecret.Namespace, Name: DefaultSecret.Name},
			Data:       map[string][]byte{"values.yaml": []byte("_cluster:\n  image-overrides:\n  - {name: a/b}\n")},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
		if _, _, err := Load(context.Background(), c, DefaultSecret); err == nil || !strings.Contains(err.Error(), "cozy-system/cozystack-values") {
			t.Fatalf("got %v, want an error naming the Secret", err)
		}
	})
}

func TestMerge(t *testing.T) {
	own := helmv2.PostRenderer{Kustomize: &helmv2.Kustomize{Patches: []kustomize.Patch{{Patch: "own"}}}}
	previous := helmv2.PostRenderer{Kustomize: &helmv2.Kustomize{Images: []kustomize.Image{{Name: "old/image", NewName: "stale"}}}}
	images := []kustomize.Image{{Name: "a/b", NewName: "c/d"}}

	got := Merge([]helmv2.PostRenderer{own, previous}, images)
	if len(got) != 2 || got[0].Kustomize.Patches[0].Patch != "own" || got[1].Kustomize.Images[0] != images[0] {
		t.Fatalf("want the release's own renderer kept and the override last, got %+v", got)
	}

	if got := Merge([]helmv2.PostRenderer{own, previous}, nil); len(got) != 1 || got[0].Kustomize.Patches[0].Patch != "own" {
		t.Fatalf("an empty table must only drop the override renderer, got %+v", got)
	}
	if got := Merge(nil, nil); got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}
