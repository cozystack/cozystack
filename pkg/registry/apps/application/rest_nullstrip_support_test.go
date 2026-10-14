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
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// The null-normalization admission test drives Create and Update for a
// Kubernetes Application and reads the client-facing warnings. On main these
// helpers come with the removed-fields warning, which this branch does not
// carry, so the test gets its own copy of them here.

// kubernetesKind is the Application.Kind of the Kubernetes cluster CR.
const kubernetesKind = "Kubernetes"

// fakeWarningRecorder captures client-facing admission warnings for assertions.
type fakeWarningRecorder struct{ warnings []string }

func (f *fakeWarningRecorder) AddWarning(_, text string) { f.warnings = append(f.warnings, text) }

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
