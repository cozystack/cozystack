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
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
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

// These tests pin the API error semantics of the aggregated Application resource.
//
// The registry stores an Application as a HelmRelease. Errors from that write used to be flattened with
// fmt.Errorf, which discards the metav1.Status they carry: the apiserver machinery could no longer map
// them to an HTTP status and answered a generic 500 whose body held only a message, with no reason.
//
// Creating an Application that already exists therefore returned
//
//	{"kind":"Status","apiVersion":"v1","status":"Failure","code":500,
//	 "message":"failed to create HelmRelease: helmreleases.helm.toolkit.fluxcd.io \"tenant-x\" already exists"}
//
// apierrors.IsAlreadyExists keys on the reason, so no client could tell a conflict from a retryable
// server failure — which makes an idempotent create impossible for every controller and every client-go
// caller. kubectl reported it as `Error from server` rather than `AlreadyExists`.

func statusErrorREST(t *testing.T, objects []client.Object, funcs interceptor.Funcs) *REST {
	t.Helper()
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
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objects) > 0 {
		builder = builder.WithObjects(objects...)
	}
	return &REST{
		c: builder.WithInterceptorFuncs(funcs).Build(),
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
		kindName: "MySQL",
		releaseConfig: config.ReleaseConfig{
			Prefix: "mysql-",
		},
	}
}

func statusErrorApp() *appsv1alpha1.Application {
	return &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       "MySQL",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "good-name",
			Namespace: "tenant-foo",
		},
	}
}

func existingHelmRelease() *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
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
}

// TestCreate_ReportsAlreadyExists is the regression this file exists for.
func TestCreate_ReportsAlreadyExists(t *testing.T) {
	r := statusErrorREST(t, []client.Object{existingHelmRelease()}, interceptor.Funcs{})
	ctx := request.WithNamespace(context.Background(), "tenant-foo")

	_, err := r.Create(ctx, statusErrorApp(), nil, &metav1.CreateOptions{})
	if err == nil {
		t.Fatal("creating an Application whose HelmRelease already exists returned no error")
	}
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("error is not AlreadyExists: %v", err)
	}

	// The conflict is reported on the resource the caller named, not on the HelmRelease backing it: a
	// client that sees `helmreleases.helm.toolkit.fluxcd.io` cannot match it against what it asked for.
	status, ok := err.(apierrors.APIStatus)
	if !ok {
		t.Fatalf("error carries no Status: %v", err)
	}
	details := status.Status().Details
	if details == nil {
		t.Fatalf("status carries no details: %v", status.Status())
	}
	if details.Kind != "mysqls" || details.Group != appsv1alpha1.GroupName {
		t.Errorf("conflict reported on %s.%s, want mysqls.%s", details.Kind, details.Group,
			appsv1alpha1.GroupName)
	}
	if details.Name != "good-name" {
		t.Errorf("conflict reported on name %q, want good-name", details.Name)
	}
	if code := status.Status().Code; code != 409 {
		t.Errorf("status code %d, want 409", code)
	}
}

// TestDelete_ReportsNotFoundLostRace covers the HelmRelease disappearing between the registry's read and
// its delete. Flattening the error turned that race into a 500, so a caller retrying a delete — the
// normal way to converge — could not recognise that the object was already gone.
func TestDelete_ReportsNotFoundLostRace(t *testing.T) {
	r := statusErrorREST(t, []client.Object{existingHelmRelease()}, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*helmv2.HelmRelease); ok {
				return apierrors.NewNotFound(
					schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
					obj.GetName(),
				)
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	ctx := request.WithNamespace(context.Background(), "tenant-foo")

	_, _, err := r.Delete(ctx, "good-name", nil, &metav1.DeleteOptions{})
	if err == nil {
		t.Fatal("deleting an Application whose HelmRelease vanished returned no error")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("error is not NotFound: %v", err)
	}
}

// TestCreate_KeepsWrappingUntypedErrors is the negative control: only errors that carry an API status
// are restated. An arbitrary failure has no semantics the API contract defines, and turning it into a
// conflict would be worse than reporting it as a server error.
func TestCreate_KeepsWrappingUntypedErrors(t *testing.T) {
	r := statusErrorREST(t, nil, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*helmv2.HelmRelease); ok {
				return errors.New("dial tcp: connection refused")
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	ctx := request.WithNamespace(context.Background(), "tenant-foo")

	_, err := r.Create(ctx, statusErrorApp(), nil, &metav1.CreateOptions{})
	if err == nil {
		t.Fatal("an untyped client failure returned no error")
	}
	if apierrors.IsAlreadyExists(err) || apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		t.Fatalf("an untyped failure was restated as an API conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to create HelmRelease") {
		t.Errorf("error lost its context: %v", err)
	}
}
