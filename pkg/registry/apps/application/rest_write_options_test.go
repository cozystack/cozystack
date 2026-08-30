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
	"fmt"
	"reflect"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

const optionsTestNamespace = "tenant-options"

func newOptionsTestREST(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register Cozystack API scheme: %v", err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register HelmRelease scheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithInterceptorFuncs(funcs).
		Build()
	return NewREST(c, nil, &config.Resource{
		Application: config.ApplicationConfig{
			Kind:     "PostgreSQL",
			Plural:   "postgresqls",
			Singular: "postgresql",
		},
		Release: config.ReleaseConfig{Prefix: "postgresql-"},
	})
}

func optionsTestApplication() *appsv1alpha1.Application {
	return &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: "PostgreSQL"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: optionsTestNamespace,
		},
	}
}

func optionsTestHelmRelease() *helmv2.HelmRelease {
	return &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Name:      "postgresql-example",
		Namespace: optionsTestNamespace,
		Labels: map[string]string{
			ApplicationKindLabel:  "PostgreSQL",
			ApplicationGroupLabel: appsv1alpha1.GroupName,
			ApplicationNameLabel:  "example",
		},
	}}
}

func captureCreateOptions(opts ...client.CreateOption) *metav1.CreateOptions {
	converted := (&client.CreateOptions{}).ApplyOptions(opts)
	return converted.AsCreateOptions().DeepCopy()
}

func captureUpdateOptions(opts ...client.UpdateOption) *metav1.UpdateOptions {
	converted := (&client.UpdateOptions{}).ApplyOptions(opts)
	return converted.AsUpdateOptions().DeepCopy()
}

func captureDeleteOptions(opts ...client.DeleteOption) *metav1.DeleteOptions {
	converted := &client.DeleteOptions{}
	for _, option := range opts {
		option.ApplyToDelete(converted)
	}
	return converted.AsDeleteOptions().DeepCopy()
}

func TestCreatePropagatesWriteOptions(t *testing.T) {
	want := &metav1.CreateOptions{
		DryRun:          []string{metav1.DryRunAll},
		FieldManager:    "create-manager",
		FieldValidation: "Strict",
	}
	expected := want.DeepCopy()
	var got *metav1.CreateOptions
	r := newOptionsTestREST(t, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.CreateOption) error {
			got = captureCreateOptions(opts...)
			return nil
		},
	})
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, err := r.Create(ctx, optionsTestApplication(), nil, want); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("Create options lost in controller-runtime conversion:\n got: %#v\nwant: %#v", got, expected)
	}
}

func TestUpdatePropagatesWriteOptions(t *testing.T) {
	want := &metav1.UpdateOptions{
		DryRun:          []string{metav1.DryRunAll},
		FieldManager:    "update-manager",
		FieldValidation: "Warn",
	}
	expected := want.DeepCopy()
	var got *metav1.UpdateOptions
	r := newOptionsTestREST(t, interceptor.Funcs{
		Update: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.UpdateOption) error {
			got = captureUpdateOptions(opts...)
			return nil
		},
	}, optionsTestHelmRelease())
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Update(ctx, "example", rest.DefaultUpdatedObjectInfo(optionsTestApplication()), nil, nil, false, want); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("Update options lost in controller-runtime conversion:\n got: %#v\nwant: %#v", got, expected)
	}
}

func TestUpdateForceCreatePropagatesWriteOptions(t *testing.T) {
	update := &metav1.UpdateOptions{
		DryRun:          []string{metav1.DryRunAll},
		FieldManager:    "apply-manager",
		FieldValidation: "Strict",
	}
	want := &metav1.CreateOptions{
		DryRun:          update.DryRun,
		FieldManager:    update.FieldManager,
		FieldValidation: update.FieldValidation,
	}
	var got *metav1.CreateOptions
	r := newOptionsTestREST(t, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.CreateOption) error {
			got = captureCreateOptions(opts...)
			return nil
		},
	})
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Update(ctx, "example", rest.DefaultUpdatedObjectInfo(optionsTestApplication()), nil, nil, true, update); err != nil {
		t.Fatalf("force-create Update returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("force-create options lost in controller-runtime conversion:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestDeletePropagatesWriteOptions(t *testing.T) {
	grace := int64(3)
	propagation := metav1.DeletePropagationForeground
	uid := types.UID("expected-uid")
	rv := "17"
	want := &metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
		Preconditions:      &metav1.Preconditions{UID: &uid, ResourceVersion: &rv},
		PropagationPolicy:  &propagation,
		DryRun:             []string{metav1.DryRunAll},
	}
	expected := want.DeepCopy()
	var got *metav1.DeleteOptions
	r := newOptionsTestREST(t, interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.DeleteOption) error {
			got = captureDeleteOptions(opts...)
			return nil
		},
	}, optionsTestHelmRelease())
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Delete(ctx, "example", nil, want); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("Delete options lost in controller-runtime conversion:\n got: %#v\nwant: %#v", got, expected)
	}
}

func TestDeletePreservesConflictError(t *testing.T) {
	conflict := apierrors.NewConflict(
		schema.GroupResource{Group: helmv2.GroupVersion.Group, Resource: "helmreleases"},
		"postgresql-example",
		fmt.Errorf("precondition failed"),
	)
	r := newOptionsTestREST(t, interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.DeleteOption) error {
			return conflict
		},
	}, optionsTestHelmRelease())
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Delete(ctx, "example", nil, &metav1.DeleteOptions{}); !apierrors.IsConflict(err) {
		t.Fatalf("Delete returned %v, want a preserved Conflict", err)
	}
}
