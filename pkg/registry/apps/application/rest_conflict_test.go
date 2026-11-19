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

func TestUpdate_RetriesOnResourceVersionConflict(t *testing.T) {
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

	// Pre-populate the HelmRelease so Update takes the update path (not the
	// forceAllowCreate upsert path).
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

	var updateCalls int
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*helmv2.HelmRelease); ok {
					updateCalls++
					if updateCalls == 1 {
						// Mimic the helm-controller racing the write: a 409 on
						// the stale resourceVersion. IsConflict only keys on the
						// status reason, so the GroupResource here is cosmetic.
						return apierrors.NewConflict(
							schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
							obj.GetName(),
							errors.New("simulated stale resourceVersion"),
						)
					}
				}
				return c.Update(ctx, obj, opts...)
			},
		}).
		Build()

	r := &REST{
		c: fakeClient,
		w: fakeClient,
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

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	_, _, err := r.Update(
		ctx,
		"good-name",
		newDefaultUpdatedObjectInfo(app),
		nil,   // createValidation
		nil,   // updateValidation
		false, // forceAllowCreate=false: stay on the Update path
		&metav1.UpdateOptions{},
	)
	if err != nil {
		t.Fatalf("expected Update to succeed after retrying the conflict, got %v", err)
	}
	if updateCalls != 2 {
		t.Errorf("expected exactly 2 HelmRelease Update attempts (1 conflict + 1 success), got %d", updateCalls)
	}
}
