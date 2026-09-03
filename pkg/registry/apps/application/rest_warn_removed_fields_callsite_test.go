/*
Copyright 2024 The Cozystack Authors.

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
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/warning"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// The Phase 2 inert-field warning is only useful if it is actually wired into
// the write path. TestWarnRemovedKubernetesFields (rest_validation_test.go)
// pins the helper in isolation, but deleting either call site
// (rest.go Create / Update) leaves that test green while an operator editing a
// removed field silently gets no warning. These two tests drive Create and
// Update end to end and assert the warning surfaces, so removing a call site
// fails loudly.

func newKubernetesWarnREST(t *testing.T, objects ...client.Object) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{
			{Application: config.ApplicationConfig{Kind: kubernetesKind}},
		},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &REST{
		c: fakeClient,
		w: fakeClient,
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "kuberneteses",
		},
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    kubernetesKind,
		},
		kindName:      kubernetesKind,
		releaseConfig: config.ReleaseConfig{Prefix: "kubernetes-"},
	}
}

func kubernetesAppWithNodeGroups() *appsv1alpha1.Application {
	return &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       kubernetesKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "good-name",
			Namespace: "tenant-foo",
		},
		Spec: &apiextv1.JSON{Raw: []byte(`{"version":"v1.35","nodeGroups":{"md0":{"minReplicas":1}}}`)},
	}
}

func TestCreate_WarnsOnRemovedKubernetesFields(t *testing.T) {
	r := newKubernetesWarnREST(t)
	app := kubernetesAppWithNodeGroups()

	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(request.WithNamespace(context.Background(), "tenant-foo"), rec)

	// Short-circuit right after the warning (createValidation runs immediately
	// after warnRemovedKubernetesFields), so the test does not depend on
	// conversion or the fake client's write path.
	sentinel := errors.New("stop after warning")
	createValidation := func(_ context.Context, _ runtime.Object) error { return sentinel }

	if _, err := r.Create(ctx, app, createValidation, &metav1.CreateOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error after warning, got %v", err)
	}
	assertNodeGroupsWarning(t, rec)
}

func TestUpdate_WarnsOnRemovedKubernetesFields(t *testing.T) {
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes-good-name",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:  kubernetesKind,
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "good-name",
			},
		},
	}
	r := newKubernetesWarnREST(t, existing)
	app := kubernetesAppWithNodeGroups()

	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(request.WithNamespace(context.Background(), "tenant-foo"), rec)

	// updateValidation runs BEFORE warnRemovedKubernetesFields, so it must pass
	// (return nil) for the warning to be reached. The final Update result is
	// irrelevant — the warning is recorded before any conversion/write — so any
	// downstream error is ignored; only the recorded warning is asserted.
	updateValidation := func(_ context.Context, _, _ runtime.Object) error { return nil }
	_, _, _ = r.Update(
		ctx,
		"good-name",
		newDefaultUpdatedObjectInfo(app),
		nil,
		updateValidation,
		false,
		&metav1.UpdateOptions{},
	)
	assertNodeGroupsWarning(t, rec)
}

func assertNodeGroupsWarning(t *testing.T, rec *fakeWarningRecorder) {
	t.Helper()
	for _, w := range rec.warnings {
		if strings.Contains(w, "spec.nodeGroups is ignored") {
			return
		}
	}
	t.Fatalf("expected a spec.nodeGroups admission warning, got %v", rec.warnings)
}
