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

package reservation

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func instancetypeObject(name string, cpu int64, memory string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "instancetype.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineClusterInstancetype",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"cpu":    map[string]any{"guest": cpu},
			"memory": map[string]any{"guest": memory},
		},
	}}
}

type clusterFixture struct {
	env   *ClusterEnvironment
	dyn   *dynamicfake.FakeDynamicClient
	lists *int
	clock *time.Time
}

func newClusterFixture(objs ...runtime.Object) clusterFixture {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{InstancetypeGVR: "VirtualMachineClusterInstancetypeList"}, objs...)
	lists := 0
	dyn.PrependReactor("list", InstancetypeGVR.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		return false, nil, nil
	})
	clock := time.Unix(0, 0)
	env := NewClusterEnvironment(dyn, ShippedPresets())
	env.now = func() time.Time { return clock }
	return clusterFixture{env: env, dyn: dyn, lists: &lists, clock: &clock}
}

func TestClusterEnvironmentResolvesInstancetypes(t *testing.T) {
	f := newClusterFixture(instancetypeObject("u1.small", 1, "4Gi"))

	rl, err := f.env.Instancetype("u1.small")
	if err != nil {
		t.Fatal(err)
	}
	if got := rl[KeyMemory]; got.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Fatalf("memory = %s, want 4Gi", got.String())
	}

	if _, err := f.env.Instancetype("u1.small"); err != nil {
		t.Fatal(err)
	}
	if *f.lists != 1 {
		t.Fatalf("a second lookup within the TTL listed the cluster again: %d lists", *f.lists)
	}

	*f.clock = f.clock.Add(defaultInstancetypeTTL)
	if _, err := f.env.Instancetype("u1.small"); err != nil {
		t.Fatal(err)
	}
	if *f.lists != 2 {
		t.Fatalf("a lookup past the TTL did not refresh: %d lists", *f.lists)
	}
}

func TestClusterEnvironmentFindsATypeCreatedAfterTheSnapshot(t *testing.T) {
	f := newClusterFixture(instancetypeObject("u1.small", 1, "4Gi"))
	if _, err := f.env.Instancetype("u1.small"); err != nil {
		t.Fatal(err)
	}

	_, err := f.dyn.Resource(InstancetypeGVR).Create(context.Background(), instancetypeObject("u1.large", 4, "16Gi"), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.env.Instancetype("u1.large"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a miss right after a refresh must not list again, got %v", err)
	}
	*f.clock = f.clock.Add(instancetypeMissRefresh)
	if _, err := f.env.Instancetype("u1.large"); err != nil {
		t.Fatalf("a miss past the refresh interval must find the new type: %v", err)
	}
}

func TestClusterEnvironmentFailsOnlyTheBrokenType(t *testing.T) {
	broken := instancetypeObject("broken", 0, "4Gi")
	f := newClusterFixture(instancetypeObject("u1.small", 1, "4Gi"), broken)

	if _, err := f.env.Instancetype("u1.small"); err != nil {
		t.Fatalf("a broken type must not fail the others: %v", err)
	}
	if _, err := f.env.Instancetype("broken"); err == nil {
		t.Fatal("a type that does not classify must fail")
	}
}

func TestClusterEnvironmentReportsListErrors(t *testing.T) {
	f := newClusterFixture()
	f.dyn.PrependReactor("list", InstancetypeGVR.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	if _, err := f.env.Instancetype("u1.small"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a failed list must surface as an error, not as a missing type: %v", err)
	}
}

func TestClusterEnvironmentPresets(t *testing.T) {
	f := newClusterFixture()
	rl, err := f.env.Preset("u1.small")
	if err != nil {
		t.Fatal(err)
	}
	if got := rl[KeyCPU]; got.Cmp(resource.MustParse("1")) != 0 {
		t.Fatalf("cpu = %s, want 1", got.String())
	}
	if _, err := f.env.Preset("x9.huge"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown preset: %v", err)
	}
}
