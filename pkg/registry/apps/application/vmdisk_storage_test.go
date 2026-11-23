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
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

func newVMDiskREST(t *testing.T) *REST {
	t.Helper()
	res := shippedResource(t, "../../../../packages/system/vm-disk-rd/cozyrds/vm-disk.yaml")
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{helmv2.AddToScheme, corev1.AddToScheme, cozyv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, &config.ResourceConfig{Resources: []config.Resource{res}}); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	return NewREST(c, c, &res)
}

func vmDisk(t *testing.T, values map[string]any) *appsv1alpha1.Application {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: vmDiskKind},
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "tenant-acme"},
		Spec:       &apiextv1.JSON{Raw: raw},
	}
}

func TestVMDiskStorageCannotBeLowered(t *testing.T) {
	cases := []struct {
		name      string
		before    map[string]any
		after     map[string]any
		forbidden bool
	}{
		{name: "lowered", before: map[string]any{"storage": "10Gi"}, after: map[string]any{"storage": "5Gi"}, forbidden: true},
		{name: "unset falls back to the smaller default", before: map[string]any{"storage": "10Gi"}, after: map[string]any{}, forbidden: true},
		{name: "same size in other units", before: map[string]any{"storage": "1Gi"}, after: map[string]any{"storage": "1024Mi"}},
		{name: "grown", before: map[string]any{"storage": "10Gi"}, after: map[string]any{"storage": "20Gi"}},
		{name: "unrelated edit", before: map[string]any{"storage": "10Gi"}, after: map[string]any{"storage": "10Gi", "storageClass": "local"}},
		{name: "lowered to a byte count", before: map[string]any{"storage": "10Gi"}, after: map[string]any{"storage": json.Number("5368709120")}, forbidden: true},
		{name: "byte count lowered", before: map[string]any{"storage": json.Number("10737418240")}, after: map[string]any{"storage": "5Gi"}, forbidden: true},
		{name: "byte count grown", before: map[string]any{"storage": json.Number("5368709120")}, after: map[string]any{"storage": json.Number("10737418240")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newVMDiskREST(t)
			ctx := request.WithNamespace(context.Background(), "tenant-acme")
			if _, err := r.Create(ctx, vmDisk(t, tc.before), nil, &metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			_, _, err := r.Update(ctx, "data", rest.DefaultUpdatedObjectInfo(vmDisk(t, tc.after)), nil, nil, false, &metav1.UpdateOptions{})
			if !tc.forbidden {
				if err != nil {
					t.Fatalf("want the update accepted, got %v", err)
				}
				return
			}
			if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "spec.storage") || !strings.Contains(err.Error(), "cannot be shrunk") {
				t.Fatalf("want spec.storage refused as a shrink, got %v", err)
			}
		})
	}
}

func TestVMDiskStorageIsCheckedAgainOnAConflictRetry(t *testing.T) {
	res := shippedResource(t, "../../../../packages/system/vm-disk-rd/cozyrds/vm-disk.yaml")
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{helmv2.AddToScheme, corev1.AddToScheme, cozyv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, &config.ResourceConfig{Resources: []config.Resource{res}}); err != nil {
		t.Fatal(err)
	}
	conflicted := false
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if !conflicted {
				conflicted = true
				// Another writer grows the disk to 20Gi first.
				live := &helmv2.HelmRelease{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
					return err
				}
				live.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"storage":"20Gi"}`)}
				if err := c.Update(ctx, live); err != nil {
					return err
				}
				return apierrors.NewConflict(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), obj.GetName(), fmt.Errorf("stale"))
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	r := NewREST(c, c, &res)
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	if _, err := r.Create(ctx, vmDisk(t, map[string]any{"storage": "10Gi"}), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.Update(ctx, "data", rest.DefaultUpdatedObjectInfo(vmDisk(t, map[string]any{"storage": "15Gi"})), nil, nil, false, &metav1.UpdateOptions{})
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "cannot be shrunk") {
		t.Fatalf("15Gi over a disk grown to 20Gi meanwhile is a shrink; got %v", err)
	}
}
