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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/validation"
	"github.com/cozystack/cozystack/pkg/config"
)

// TestCreate_AdmissionChain_RejectsViaCreateValidation pins the
// admission-chain wiring at REST.Create. genericregistry.Store invokes
// the createValidation function automatically; custom REST handlers
// like this one must do it explicitly. Without that explicit call,
// every ValidatingAdmissionPolicy targeting Application is silently
// bypassed on Create and Delete — meaning every Layer-2-7 VAP that
// reads Application or its derived state is unenforced for any
// `kubectl create application` command.
//
// The test wires a sentinel-returning createValidation through Create
// and asserts the sentinel propagates. A regression that drops the
// createValidation call (or short-circuits past it) makes this test
// fail loudly.
func TestCreate_AdmissionChain_RejectsViaCreateValidation(t *testing.T) {
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
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
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
		kindName: "MySQL",
		releaseConfig: config.ReleaseConfig{
			Prefix: "mysql-",
		},
	}

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

	sentinel := errors.New("simulated VAP rejection")
	createValidation := func(_ context.Context, _ runtime.Object) error {
		return sentinel
	}

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	_, err := r.Create(ctx, app, createValidation, &metav1.CreateOptions{})
	if err == nil {
		t.Fatalf("expected Create to surface admission error, got nil")
	}
	if !errors.Is(err, sentinel) && !strings.Contains(err.Error(), sentinel.Error()) {
		t.Errorf("expected sentinel admission error to propagate, got %v", err)
	}
}

// TestUpdate_AdmissionChain_RejectsViaUpdateValidation pins the
// admission-chain wiring on the Update path. Update was already
// invoking updateValidation correctly before this PR, but a future
// refactor that breaks it would silently bypass every VAP targeting
// Application on Update — same regression risk Create and Delete
// just had. Keeping all three verbs pinned uniformly keeps the
// admission-chain contract explicit.
func TestUpdate_AdmissionChain_RejectsViaUpdateValidation(t *testing.T) {
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

	// Pre-populate a HelmRelease so Update finds an existing object
	// (otherwise Update routes to Create via forceAllowCreate, hitting
	// createValidation instead of updateValidation).
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
		kindName: "MySQL",
		releaseConfig: config.ReleaseConfig{
			Prefix: "mysql-",
		},
	}

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

	sentinel := errors.New("simulated update VAP rejection")
	updateValidation := func(_ context.Context, _ runtime.Object, _ runtime.Object) error {
		return sentinel
	}

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	_, _, err := r.Update(
		ctx,
		"good-name",
		// rest.DefaultUpdatedObjectInfo wraps the new object so the
		// REST handler can compute the patch and feed it to
		// updateValidation. Same shape as the existing
		// rest_validation_test.go::TestUpdate_ForceAllowCreate test.
		newDefaultUpdatedObjectInfo(app),
		nil,              // createValidation (we want updateValidation path)
		updateValidation, // <-- the hook under test
		false,            // forceAllowCreate=false: Update path, not upsert
		&metav1.UpdateOptions{},
	)
	if err == nil {
		t.Fatalf("expected Update to surface admission error, got nil")
	}
	if !errors.Is(err, sentinel) && !strings.Contains(err.Error(), sentinel.Error()) {
		t.Errorf("expected sentinel admission error to propagate, got %v", err)
	}
}

func newDefaultUpdatedObjectInfo(obj runtime.Object) restUpdatedObjectInfo {
	return restUpdatedObjectInfo{obj: obj}
}

type restUpdatedObjectInfo struct {
	obj runtime.Object
}

func (r restUpdatedObjectInfo) Preconditions() *metav1.Preconditions { return nil }
func (r restUpdatedObjectInfo) UpdatedObject(_ context.Context, _ runtime.Object) (runtime.Object, error) {
	return r.obj, nil
}

// TestDelete_AdmissionChain_RejectsViaDeleteValidation pins the same
// contract on the Delete path. The deleteValidation fn is the hook
// genericapiserver supplies for VAP Deletion-time enforcement; without
// the explicit call site at rest.go's Delete handler, deletion-time
// admission is bypassed.
func TestDelete_AdmissionChain_RejectsViaDeleteValidation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{
			{Application: config.ApplicationConfig{Kind: validation.TenantKind}},
		},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}

	// Pre-populate a HelmRelease so the resolution step succeeds and we
	// reach the deleteValidation call — without an existing object,
	// Delete returns NotFound before invoking the hook.
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-foo",
			Namespace: "tenant-root",
			Labels: map[string]string{
				ApplicationKindLabel:  validation.TenantKind,
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "foo",
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	r := &REST{
		c: fakeClient,
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenants",
		},
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    validation.TenantKind,
		},
		kindName: validation.TenantKind,
		releaseConfig: config.ReleaseConfig{
			Prefix: "tenant-",
		},
	}

	sentinel := errors.New("simulated delete VAP rejection")
	deleteValidation := func(_ context.Context, _ runtime.Object) error {
		return sentinel
	}

	ctx := request.WithNamespace(context.Background(), "tenant-root")
	_, _, err := r.Delete(ctx, "foo", deleteValidation, &metav1.DeleteOptions{})
	if err == nil {
		t.Fatalf("expected Delete to surface admission error, got nil")
	}
	if !errors.Is(err, sentinel) && !strings.Contains(err.Error(), sentinel.Error()) {
		t.Errorf("expected sentinel admission error to propagate, got %v", err)
	}

}
