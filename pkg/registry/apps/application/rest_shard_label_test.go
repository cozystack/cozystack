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
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fluxshard "github.com/cozystack/cozystack/internal/fluxshardoperator"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// TestUpdate_PreservesShardKeyLabel pins the flux-shard-operator contract on
// the Update path: the operator owns sharding.fluxcd.io/key at runtime, and
// rebuilding the HelmRelease from the Application must not revert the live
// value to the ApplicationDefinition default. A regression here bounces every
// updated HelmRelease off its shard back to the legacy "tenants" bucket — the
// orphaning class the operator's drain guard exists to prevent.
func TestUpdate_PreservesShardKeyLabel(t *testing.T) {
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

	// The live HelmRelease has been assigned to shard3 by the operator.
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mysql-good-name",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:    "MySQL",
				ApplicationGroupLabel:   appsv1alpha1.GroupName,
				ApplicationNameLabel:    "good-name",
				fluxshard.ShardKeyLabel: "shard3",
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
			// The ApplicationDefinition default every HelmRelease is born
			// with; without preservation this would overwrite shard3.
			Labels: map[string]string{fluxshard.ShardKeyLabel: fluxshard.LegacyShardKey},
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
	if _, _, err := r.Update(ctx, "good-name", newDefaultUpdatedObjectInfo(app),
		nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := fakeClient.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "mysql-good-name"}, got); err != nil {
		t.Fatalf("fetch updated HelmRelease: %v", err)
	}
	if v := got.Labels[fluxshard.ShardKeyLabel]; v != "shard3" {
		t.Errorf("expected live shard label to be preserved on Update, got %s=%q", fluxshard.ShardKeyLabel, v)
	}
}
