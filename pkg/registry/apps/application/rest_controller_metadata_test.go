/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package application

import (
	"context"
	"errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// TestUpdate_PreservesControllerFinalizersAndAnnotations pins the other half of
// the contract TestUpdate_PreservesShardKeyLabel covers for one label.
//
// Update is a full PUT of a HelmRelease rebuilt from the Application, so
// anything a controller wrote on the live object is dropped unless carried
// over. Flux puts finalizers.fluxcd.io on every HelmRelease it manages, and the
// site-router controller adds a finalizer plus a route-ownership annotation it
// reads back on the next reconcile.
//
// A tenant edit that strips them is not cosmetic. Edit then delete, and the
// object goes away with no finalizer left to run cleanup on: the gateway's
// entries stay in the namespace routes annotation pointing at a dead pod IP,
// every pod created after that inherits a blackhole route, and a replacement
// instance declaring the same network can never take ownership back. Losing the
// ownership annotation alone does the same thing on the next gateway pod IP
// change, with no delete involved.
func TestUpdate_PreservesControllerFinalizersAndAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{
			{Application: config.ApplicationConfig{Kind: "MySQL"}},
		},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}

	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mysql-good-name",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:  "MySQL",
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "good-name",
			},
			Finalizers: []string{
				"finalizers.fluxcd.io/helm-release",
				"apps.cozystack.io/site-router-mediation",
			},
			Annotations: map[string]string{
				// Written by a controller, unprefixed, must survive.
				"apps.cozystack.io/site-router-route-gateway-ip": "10.244.0.5",
				// The tenant's own, mirrored from the Application, may be dropped
				// when the Application no longer carries it.
				AnnotationPrefix + "stale": "gone",
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	r := &REST{
		c: fakeClient,
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "mysqls",
		},
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    "MySQL",
		},
		kindName:      "MySQL",
		releaseConfig: config.ReleaseConfig{Prefix: "mysql-"},
	}

	// An ordinary tenant edit: the Application carries none of the above.
	app := &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       "MySQL",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "good-name",
			Namespace: "tenant-foo",
		},
	}

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	if _, _, err := r.Update(ctx, "good-name", newDefaultUpdatedObjectInfo(app),
		nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := fakeClient.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "mysql-good-name"}, got); err != nil {
		t.Fatalf("fetch updated HelmRelease: %v", err)
	}

	for _, want := range []string{
		"finalizers.fluxcd.io/helm-release",
		"apps.cozystack.io/site-router-mediation",
	} {
		var found bool
		for _, f := range got.Finalizers {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Errorf("finalizer %q was dropped by a tenant spec update; cleanup can no longer run, got %v", want, got.Finalizers)
		}
	}

	if v := got.Annotations["apps.cozystack.io/site-router-route-gateway-ip"]; v != "10.244.0.5" {
		t.Errorf("controller-written annotation was dropped by a tenant spec update, got %q", v)
	}

	// The conversion owns the prefixed ones, so an annotation the Application no
	// longer carries is correctly gone. Preserving those instead would make a
	// tenant-removed annotation unremovable.
	if _, still := got.Annotations[AnnotationPrefix+"stale"]; still {
		t.Errorf("a prefixed annotation the Application no longer carries must not be resurrected, got %v", got.Annotations)
	}
}

// TestUpdate_PreservesMetadataAcrossConflictRetry is the same contract on the
// path where it is most likely to matter. A 409 means somebody wrote the object
// between this handler's read and its PUT, and the writers are exactly the
// controllers whose metadata the carry-over exists to preserve. Refreshing only
// the resourceVersion and re-sending the object built from the stale read drops
// whatever they had just added.
func TestUpdate_PreservesMetadataAcrossConflictRetry(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{
			{Application: config.ApplicationConfig{Kind: "MySQL"}},
		},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}

	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mysql-good-name",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:  "MySQL",
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "good-name",
			},
		},
	}

	var built client.WithWatch
	firstUpdate := true
	built = fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if !firstUpdate {
					return c.Update(ctx, obj, opts...)
				}
				firstUpdate = false
				// The controller wins the race: it adds its finalizer and its
				// ownership annotation to the live object, which is what makes
				// this handler's write conflict.
				live := &helmv2.HelmRelease{}
				if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "mysql-good-name"}, live); err != nil {
					return err
				}
				live.Finalizers = append(live.Finalizers, "apps.cozystack.io/site-router-mediation")
				if live.Annotations == nil {
					live.Annotations = map[string]string{}
				}
				live.Annotations["apps.cozystack.io/site-router-route-gateway-ip"] = "10.244.0.5"
				if err := c.Update(ctx, live); err != nil {
					return err
				}
				return apierrors.NewConflict(
					schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
					"mysql-good-name", errors.New("object has been modified"))
			},
		}).Build()

	r := &REST{
		c: built,
		gvr: schema.GroupVersionResource{
			Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: "mysqls",
		},
		gvk: schema.GroupVersionKind{
			Group: appsv1alpha1.GroupName, Version: "v1alpha1", Kind: "MySQL",
		},
		kindName:      "MySQL",
		releaseConfig: config.ReleaseConfig{Prefix: "mysql-"},
	}

	app := &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: "MySQL"},
		ObjectMeta: metav1.ObjectMeta{Name: "good-name", Namespace: "tenant-foo"},
	}

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	if _, _, err := r.Update(ctx, "good-name", newDefaultUpdatedObjectInfo(app),
		nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := built.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "mysql-good-name"}, got); err != nil {
		t.Fatalf("fetch updated HelmRelease: %v", err)
	}

	var found bool
	for _, f := range got.Finalizers {
		if f == "apps.cozystack.io/site-router-mediation" {
			found = true
		}
	}
	if !found {
		t.Errorf("a finalizer added during the conflict window must survive the retry, got %v", got.Finalizers)
	}
	if v := got.Annotations["apps.cozystack.io/site-router-route-gateway-ip"]; v != "10.244.0.5" {
		t.Errorf("an annotation written during the conflict window must survive the retry, got %q", v)
	}
}
