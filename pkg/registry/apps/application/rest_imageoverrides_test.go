package application

import (
	"context"
	"errors"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var testOverride = kustomize.Image{
	Name:    "ghcr.io/cloudnative-pg/postgresql",
	NewName: "registry.example.org/os/postgresql",
	NewTag:  "17.7",
}

func withOverrides(images []kustomize.Image, err error) func(context.Context) ([]kustomize.Image, bool, error) {
	return func(context.Context) ([]kustomize.Image, bool, error) { return images, true, err }
}

func withoutOverridesSecret(context.Context) ([]kustomize.Image, bool, error) {
	return nil, false, nil
}

func assertOverridePostRenderer(t *testing.T, hr *helmv2.HelmRelease) {
	t.Helper()
	if len(hr.Spec.PostRenderers) != 1 || hr.Spec.PostRenderers[0].Kustomize == nil {
		t.Fatalf("want one kustomize post-renderer, got %+v", hr.Spec.PostRenderers)
	}
	images := hr.Spec.PostRenderers[0].Kustomize.Images
	if len(images) != 1 || images[0] != testOverride {
		t.Fatalf("want %+v, got %+v", testOverride, images)
	}
}

func TestCreate_AppliesImageOverrides(t *testing.T) {
	r, c := newSuspendTestREST(t, &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "tenant-foo"}}, nil)
	r.imageOverrides = withOverrides([]kustomize.Image{testOverride}, nil)

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	if _, err := r.Create(ctx, suspendTestApplication(), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := &helmv2.HelmRelease{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "postgres-pg"}, got); err != nil {
		t.Fatalf("fetch HelmRelease: %v", err)
	}
	assertOverridePostRenderer(t, got)
}

// An update rebuilds the spec from the Application, but post-renderers are not
// part of it: the release keeps the ones it carries of its own, ahead of the
// current table, which replaces the previous one and goes once it is emptied.
// A deleted values Secret says nothing about the table, so the release then
// keeps everything it carries.
func TestUpdate_AppliesImageOverrides(t *testing.T) {
	own := helmv2.PostRenderer{Kustomize: &helmv2.Kustomize{Patches: []kustomize.Patch{{Patch: "own"}}}}
	stale := helmv2.PostRenderer{Kustomize: &helmv2.Kustomize{Images: []kustomize.Image{{Name: "old/image", NewName: "stale"}}}}
	current := helmv2.PostRenderer{Kustomize: &helmv2.Kustomize{Images: []kustomize.Image{testOverride}}}
	for name, tc := range map[string]struct {
		load func(context.Context) ([]kustomize.Image, bool, error)
		want []helmv2.PostRenderer
	}{
		"table set":      {load: withOverrides([]kustomize.Image{testOverride}, nil), want: []helmv2.PostRenderer{own, current}},
		"table emptied":  {load: withOverrides(nil, nil), want: []helmv2.PostRenderer{own}},
		"secret missing": {load: withoutOverridesSecret, want: []helmv2.PostRenderer{own, stale}},
	} {
		t.Run(name, func(t *testing.T) {
			existing := suspendTestHelmRelease(false)
			existing.Spec.PostRenderers = []helmv2.PostRenderer{own, stale}
			r, c := newSuspendTestREST(t, existing, nil)
			r.imageOverrides = tc.load

			ctx := request.WithNamespace(context.Background(), "tenant-foo")
			if _, _, err := r.Update(ctx, "pg", newDefaultUpdatedObjectInfo(suspendTestApplication()),
				nil, nil, false, &metav1.UpdateOptions{}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			got := &helmv2.HelmRelease{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "postgres-pg"}, got); err != nil {
				t.Fatalf("fetch HelmRelease: %v", err)
			}
			if !apiequality.Semantic.DeepEqual(got.Spec.PostRenderers, tc.want) {
				t.Fatalf("want post-renderers %+v, got %+v", tc.want, got.Spec.PostRenderers)
			}
		})
	}
}

// A release written without the table would pull images an air-gapped cluster
// cannot reach, so a table that cannot be read refuses the write.
func TestCreate_RefusesUnreadableImageOverrides(t *testing.T) {
	r, c := newSuspendTestREST(t, &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "tenant-foo"}}, nil)
	r.imageOverrides = withOverrides(nil, errors.New("broken table"))

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	_, err := r.Create(ctx, suspendTestApplication(), nil, &metav1.CreateOptions{})
	if !apierrors.IsInternalError(err) {
		t.Fatalf("want an internal error, got %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "postgres-pg"}, &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
		t.Fatalf("want no HelmRelease, got %v", err)
	}
}

// A retry after a conflict merges into the release as it is now, so a
// post-renderer the conflicting writer added survives it.
func TestUpdate_MergesImageOverridesAfterConflict(t *testing.T) {
	added := helmv2.PostRenderer{Kustomize: &helmv2.Kustomize{Patches: []kustomize.Patch{{Patch: "added during the conflict"}}}}
	var updates int
	r := newOptionsTestREST(t, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updates++
			if updates > 1 {
				return c.Update(ctx, obj, opts...)
			}
			live := &helmv2.HelmRelease{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: optionsTestNamespace, Name: "postgresql-example"}, live); err != nil {
				return err
			}
			live.Spec.PostRenderers = []helmv2.PostRenderer{added}
			if err := c.Update(ctx, live); err != nil {
				return err
			}
			return apierrors.NewConflict(
				schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
				"postgresql-example", errors.New("object was modified"))
		},
	}, optionsTestHelmRelease())
	r.imageOverrides = withOverrides([]kustomize.Image{testOverride}, nil)

	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Update(ctx, "example", newDefaultUpdatedObjectInfo(optionsTestApplication()),
		nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updates < 2 {
		t.Fatalf("expected the conflict to be retried, got %d update calls", updates)
	}
	got := &helmv2.HelmRelease{}
	if err := r.c.Get(ctx, client.ObjectKey{Namespace: optionsTestNamespace, Name: "postgresql-example"}, got); err != nil {
		t.Fatalf("fetch HelmRelease: %v", err)
	}
	want := []helmv2.PostRenderer{added, {Kustomize: &helmv2.Kustomize{Images: []kustomize.Image{testOverride}}}}
	if !apiequality.Semantic.DeepEqual(got.Spec.PostRenderers, want) {
		t.Fatalf("want post-renderers %+v, got %+v", want, got.Spec.PostRenderers)
	}
}

// The table is read again after every read of the live release, so a retry
// after a conflict writes the table as it is then, not the one the first
// attempt read.
func TestUpdate_RereadsImageOverridesAfterConflict(t *testing.T) {
	older := kustomize.Image{Name: "ghcr.io/cloudnative-pg/postgresql", NewName: "registry.example.org/os/older"}
	var updates int
	r := newOptionsTestREST(t, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updates++
			if updates > 1 {
				return c.Update(ctx, obj, opts...)
			}
			return apierrors.NewConflict(
				schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
				"postgresql-example", errors.New("object was modified"))
		},
	}, optionsTestHelmRelease())
	var loads int
	r.imageOverrides = func(context.Context) ([]kustomize.Image, bool, error) {
		loads++
		if loads == 1 {
			return []kustomize.Image{older}, true, nil
		}
		return []kustomize.Image{testOverride}, true, nil
	}

	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Update(ctx, "example", newDefaultUpdatedObjectInfo(optionsTestApplication()),
		nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := &helmv2.HelmRelease{}
	if err := r.c.Get(ctx, client.ObjectKey{Namespace: optionsTestNamespace, Name: "postgresql-example"}, got); err != nil {
		t.Fatalf("fetch HelmRelease: %v", err)
	}
	want := []helmv2.PostRenderer{{Kustomize: &helmv2.Kustomize{Images: []kustomize.Image{testOverride}}}}
	if !apiequality.Semantic.DeepEqual(got.Spec.PostRenderers, want) {
		t.Fatalf("want the table read on the retry %+v, got %+v", want, got.Spec.PostRenderers)
	}
}
