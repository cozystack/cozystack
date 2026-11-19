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
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

func newSuspendTestREST(t *testing.T, existing *helmv2.HelmRelease, funcs *interceptor.Funcs) (*REST, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{
			{Application: config.ApplicationConfig{Kind: "Postgres"}},
		},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing)
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	c := builder.Build()
	return &REST{
		c: c,
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "postgreses",
		},
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    "Postgres",
		},
		kindName:      "Postgres",
		releaseConfig: config.ReleaseConfig{Prefix: "postgres-"},
	}, c
}

func suspendTestHelmRelease(suspend bool) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "postgres-pg",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:  "Postgres",
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "pg",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			Suspend: suspend,
			// Differs from the Application below, so a carry-over that took
			// more than suspension off the live object shows up as stale values.
			Values: &apiextv1.JSON{Raw: []byte(`{"replicas":5}`)},
		},
	}
}

func suspendTestApplication() *appsv1alpha1.Application {
	return &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       "Postgres",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pg",
			Namespace: "tenant-foo",
		},
		Spec: &apiextv1.JSON{Raw: []byte(`{"replicas":2}`)},
	}
}

// TestUpdate_PreservesSuspend pins that an Application edit leaves the
// release's suspension exactly as it found it. The rebuilt HelmRelease carries
// no suspension of its own, so a regression resumes a release someone
// suspended: the CNPG restore driver suspends the target release, patches the
// Postgres app through this API and purges the Cluster, and a release resumed
// by that patch renders the recovery Cluster before the purge, which then
// deletes it. The unsuspended case guards the other direction, a fix that
// suspends every release it touches.
func TestUpdate_PreservesSuspend(t *testing.T) {
	for _, suspend := range []bool{true, false} {
		r, c := newSuspendTestREST(t, suspendTestHelmRelease(suspend), nil)

		ctx := request.WithNamespace(context.Background(), "tenant-foo")
		if _, _, err := r.Update(ctx, "pg", newDefaultUpdatedObjectInfo(suspendTestApplication()),
			nil, nil, false, &metav1.UpdateOptions{}); err != nil {
			t.Fatalf("suspend=%v: Update failed: %v", suspend, err)
		}

		got := &helmv2.HelmRelease{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "postgres-pg"}, got); err != nil {
			t.Fatalf("suspend=%v: fetch updated HelmRelease: %v", suspend, err)
		}
		if got.Spec.Suspend != suspend {
			t.Errorf("an Application update changed spec.suspend from %v to %v", suspend, got.Spec.Suspend)
		}
		if got.Spec.Values == nil || string(got.Spec.Values.Raw) != `{"replicas":2}` {
			var raw string
			if got.Spec.Values != nil {
				raw = string(got.Spec.Values.Raw)
			}
			t.Errorf("suspend=%v: values must come from the Application, not the live object, got %s", suspend, raw)
		}
	}
}

// TestUpdate_RefreshesSuspendOnConflictRetry covers the window between Update's
// read of the live HelmRelease and its write. A 409 there means something wrote
// the release in between, and that write can be the one that suspended it: the
// restore driver suspends immediately before it patches the app. Re-sending the
// suspension from the stale read would resume the release on the retry.
//
// The interceptor suspends the stored release and then fails the first write
// with a Conflict, which is the order a real racing suspend produces.
func TestUpdate_RefreshesSuspendOnConflictRetry(t *testing.T) {
	var updateCalls int
	funcs := &interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*helmv2.HelmRelease); ok {
				updateCalls++
				if updateCalls == 1 {
					live := &helmv2.HelmRelease{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
						return err
					}
					live.Spec.Suspend = true
					if err := c.Update(ctx, live); err != nil {
						return err
					}
					return apierrors.NewConflict(
						schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
						obj.GetName(),
						errors.New("simulated suspend between read and write"),
					)
				}
			}
			return c.Update(ctx, obj, opts...)
		},
	}
	r, c := newSuspendTestREST(t, suspendTestHelmRelease(false), funcs)

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	if _, _, err := r.Update(ctx, "pg", newDefaultUpdatedObjectInfo(suspendTestApplication()),
		nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updateCalls != 2 {
		t.Fatalf("expected one conflicting write and one retry, got %d HelmRelease writes", updateCalls)
	}

	got := &helmv2.HelmRelease{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "postgres-pg"}, got); err != nil {
		t.Fatalf("fetch updated HelmRelease: %v", err)
	}
	if !got.Spec.Suspend {
		t.Errorf("the retry resumed a release that was suspended between Update's read and its write")
	}
}
