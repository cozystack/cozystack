package application

import (
	"context"
	"errors"
	"slices"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

func TestApplicationGarbageCollection(t *testing.T) {
	for _, finalizer := range []string{metav1.FinalizerDeleteDependents, metav1.FinalizerOrphanDependents} {
		for _, helmFirst := range []bool{true, false} {
			order := "gc-first"
			if helmFirst {
				order = "helm-first"
			}
			t.Run(finalizer+"/"+order, func(t *testing.T) {
				hr := optionsTestHelmRelease()
				hr.UID = "release-uid"
				now := metav1.Now()
				hr.DeletionTimestamp = &now
				hr.Finalizers = []string{finalizer}
				if !helmFirst {
					hr.Finalizers = append(hr.Finalizers, "finalizers.fluxcd.io")
				}
				r := newOptionsTestREST(t, interceptor.Funcs{}, hr)
				ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
				obj, err := r.Get(ctx, "example", &metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				app := obj.(*appsv1alpha1.Application)
				if app.UID != hr.UID || app.DeletionTimestamp == nil {
					t.Fatalf("deletion identity lost: %v", app.ObjectMeta)
				}
				if !slices.Contains(app.Finalizers, finalizer) {
					t.Fatalf("garbage collector cannot see %q: %v", finalizer, app.Finalizers)
				}
				app.Finalizers = slices.DeleteFunc(app.Finalizers, func(f string) bool { return f == finalizer })
				if _, _, err := r.Update(ctx, app.Name, rest.DefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				live := &helmv2.HelmRelease{}
				err = r.c.Get(ctx, client.ObjectKeyFromObject(hr), live)
				if helmFirst {
					if !apierrors.IsNotFound(err) {
						t.Fatalf("release still exists after cleanup: err=%v, finalizers=%v", err, live.Finalizers)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(live.Finalizers, []string{"finalizers.fluxcd.io"}) {
					t.Fatalf("Helm cleanup protection changed: %v", live.Finalizers)
				}
				live.Finalizers = nil
				if err := r.c.Update(ctx, live); err != nil {
					t.Fatal(err)
				}
				if err := r.c.Get(ctx, client.ObjectKeyFromObject(hr), live); !apierrors.IsNotFound(err) {
					t.Fatalf("release still exists after Helm cleanup: %v", err)
				}
			})
		}
	}
}

func TestApplicationGarbageCollectionConflict(t *testing.T) {
	for _, concurrent := range []struct {
		name      string
		initial   []string
		requested []string
		updated   []string
		expected  []string
	}{
		{"helm-completes", []string{metav1.FinalizerDeleteDependents, "finalizers.fluxcd.io", "test.example/hold"}, nil, []string{metav1.FinalizerDeleteDependents, "test.example/hold"}, []string{"test.example/hold"}},
		{"controller-adds-finalizer", []string{metav1.FinalizerDeleteDependents}, nil, []string{metav1.FinalizerDeleteDependents, "finalizers.fluxcd.io"}, []string{"finalizers.fluxcd.io"}},
		{"gc-starts-during-update", []string{"finalizers.fluxcd.io"}, nil, []string{"finalizers.fluxcd.io", metav1.FinalizerDeleteDependents}, []string{"finalizers.fluxcd.io", metav1.FinalizerDeleteDependents}},
		{"gc-finishes-during-update", []string{"finalizers.fluxcd.io", metav1.FinalizerDeleteDependents}, []string{metav1.FinalizerDeleteDependents}, []string{"finalizers.fluxcd.io"}, []string{"finalizers.fluxcd.io"}},
	} {
		t.Run(concurrent.name, func(t *testing.T) {
			hr := optionsTestHelmRelease()
			hr.Finalizers = concurrent.initial
			updates := 0
			r := newOptionsTestREST(t, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					if updates > 1 {
						return c.Update(ctx, obj, opts...)
					}
					live := &helmv2.HelmRelease{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(hr), live); err != nil {
						return err
					}
					live.Finalizers = slices.Clone(concurrent.updated)
					if err := c.Update(ctx, live); err != nil {
						return err
					}
					return apierrors.NewConflict(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), hr.Name, errors.New("concurrent controller update"))
				},
			}, hr)
			ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
			obj, err := r.Get(ctx, "example", &metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			app := obj.(*appsv1alpha1.Application)
			app.Finalizers = concurrent.requested
			if _, _, err := r.Update(ctx, app.Name, rest.DefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			live := &helmv2.HelmRelease{}
			if err := r.c.Get(ctx, client.ObjectKeyFromObject(hr), live); err != nil {
				t.Fatal(err)
			}
			if updates != 2 {
				t.Fatalf("update calls = %d, want 2", updates)
			}
			if !slices.Equal(live.Finalizers, concurrent.expected) {
				t.Fatalf("finalizers = %v, want %v", live.Finalizers, concurrent.expected)
			}
		})
	}
}

func TestApplicationFinalizersKeepControllerProtection(t *testing.T) {
	hr := optionsTestHelmRelease()
	hr.Finalizers = []string{"finalizers.fluxcd.io", metav1.FinalizerDeleteDependents}
	r := newOptionsTestREST(t, interceptor.Funcs{}, hr)
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	obj, err := r.Get(ctx, "example", &metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	app := obj.(*appsv1alpha1.Application)
	if !slices.Equal(app.Finalizers, []string{metav1.FinalizerDeleteDependents}) {
		t.Fatalf("unexpected application finalizers: %v", app.Finalizers)
	}
	app.Finalizers = []string{"other.example/ignored"}
	if _, _, err := r.Update(ctx, app.Name, rest.DefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	live := &helmv2.HelmRelease{}
	if err := r.c.Get(ctx, client.ObjectKeyFromObject(hr), live); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(live.Finalizers, []string{"finalizers.fluxcd.io"}) {
		t.Fatalf("controller finalizers changed: %v", live.Finalizers)
	}
}

func TestApplicationFinalizersDoNotMutateCachedHelmRelease(t *testing.T) {
	hr := optionsTestHelmRelease()
	hr.Finalizers = []string{metav1.FinalizerDeleteDependents, "finalizers.fluxcd.io"}
	r := newOptionsTestREST(t, interceptor.Funcs{})
	app, err := r.ConvertHelmReleaseToApplication(context.Background(), hr)
	if err != nil {
		t.Fatal(err)
	}
	app.Finalizers[0] = metav1.FinalizerOrphanDependents
	if !slices.Equal(hr.Finalizers, []string{metav1.FinalizerDeleteDependents, "finalizers.fluxcd.io"}) {
		t.Fatalf("conversion changed cached finalizers: %v", hr.Finalizers)
	}
}

func TestApplicationGarbageCollectionWithInPlaceUpdate(t *testing.T) {
	hr := optionsTestHelmRelease()
	hr.Finalizers = []string{metav1.FinalizerDeleteDependents}
	now := metav1.Now()
	hr.DeletionTimestamp = &now
	r := newOptionsTestREST(t, interceptor.Funcs{}, hr)
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	update := rest.DefaultUpdatedObjectInfo(nil, func(_ context.Context, _, old runtime.Object) (runtime.Object, error) {
		app := old.(*appsv1alpha1.Application)
		app.Finalizers = nil
		return app, nil
	})
	if _, _, err := r.Update(ctx, "example", update, nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.c.Get(ctx, client.ObjectKeyFromObject(hr), &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
		t.Fatalf("release still exists after in-place finalizer removal: %v", err)
	}
}
