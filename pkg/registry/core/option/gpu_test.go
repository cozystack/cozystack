// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"
	"errors"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/cozystack/cozystack/internal/gpu"
)

func gpuSettingsObj(raw gpu.RawNamesPolicy, names string) *unstructured.Unstructured {
	cm := newObj(gvrConfigMaps.GroupVersion().WithKind("ConfigMap"), gpu.SettingsNamespace, gpu.SettingsConfigMap, nil)
	_ = unstructured.SetNestedStringMap(cm.Object, map[string]string{
		gpu.SettingsKeyRawResourceNames: string(raw),
		gpu.SettingsKeyResourceNames:    names,
	}, "data")
	return cm
}

func whitelistedGPU() (*unstructured.Unstructured, *unstructured.Unstructured) {
	kv := newObj(gvrKubevirts.GroupVersion().WithKind("KubeVirt"), kubevirtNamespace, "kubevirt", map[string]any{
		"configuration": map[string]any{
			"permittedHostDevices": map[string]any{
				"pciHostDevices": []any{map[string]any{"resourceName": "nvidia.com/A100"}},
			},
		},
	})
	node := &unstructured.Unstructured{}
	node.SetGroupVersionKind(gvrNodes.GroupVersion().WithKind("Node"))
	node.SetName("node-a")
	_ = unstructured.SetNestedStringMap(node.Object, map[string]string{"nvidia.com/A100": "2"}, "status", "allocatable")
	return kv, node
}

func TestGPUProviderUnderRawNamePolicy(t *testing.T) {
	tests := []struct {
		name       string
		settings   *unstructured.Unstructured
		unreadable bool
		want       []string
	}{
		{name: "forbidden returns nothing", settings: gpuSettingsObj(gpu.RawNamesForbidden, "nvidia.com/A100")},
		{name: "allowed serves raw names", settings: gpuSettingsObj(gpu.RawNamesAllowed, "nvidia.com/A100"), want: []string{"nvidia.com/A100"}},
		{name: "no settings serves raw names", want: []string{"nvidia.com/A100"}},
		{
			name:       "unreadable settings serve the full list",
			settings:   gpuSettingsObj(gpu.RawNamesForbidden, "nvidia.com/A100,nvidia.com/A400"),
			unreadable: true,
			want:       []string{"nvidia.com/A100"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kv, node := whitelistedGPU()
			objs := []runtime.Object{kv, node}
			if tc.settings != nil {
				objs = append(objs, tc.settings)
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...)
			if tc.unreadable {
				dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("simulated API outage")
				})
			}
			items, err := DefaultProviders(dyn)["gpu"](context.Background(), "tenant-foo")
			if err != nil {
				t.Fatalf("gpu provider: %v", err)
			}
			if got := values(items); !slices.Equal(got, tc.want) {
				t.Fatalf("gpu: got %v, want %v", got, tc.want)
			}
		})
	}
}

func instancetype(name string, spec map[string]any) *unstructured.Unstructured {
	return newObj(gvrInstancetype.GroupVersion().WithKind("VirtualMachineClusterInstancetype"), "", name, spec)
}

func TestInstancetypeProviderUnderRawNamePolicy(t *testing.T) {
	types := []runtime.Object{
		instancetype("u1.medium", map[string]any{"cpu": map[string]any{"guest": int64(1)}}),
		instancetype("gn1.xlarge", map[string]any{"gpus": []any{map[string]any{"name": "gpu1", "deviceName": "nvidia.com/A400"}}}),
		instancetype("hd1.large", map[string]any{"hostDevices": []any{map[string]any{"name": "gpu1", "deviceName": "nvidia.com/A100"}}}),
		instancetype("rdma1.large", map[string]any{"hostDevices": []any{map[string]any{"name": "nic1", "deviceName": "nvidia.com/hostdev"}}}),
	}
	tests := []struct {
		name       string
		settings   *unstructured.Unstructured
		unreadable bool
		want       []string
	}{
		{
			name:     "forbidden hides instance types with GPU entries",
			settings: gpuSettingsObj(gpu.RawNamesForbidden, "nvidia.com/A100,nvidia.com/A400"),
			want:     []string{"rdma1.large", "u1.medium"},
		},
		{
			name:     "allowed lists every instance type",
			settings: gpuSettingsObj(gpu.RawNamesAllowed, "nvidia.com/A100,nvidia.com/A400"),
			want:     []string{"gn1.xlarge", "hd1.large", "rdma1.large", "u1.medium"},
		},
		{
			name: "no settings lists every instance type",
			want: []string{"gn1.xlarge", "hd1.large", "rdma1.large", "u1.medium"},
		},
		{
			name:       "unreadable settings serve the full list",
			settings:   gpuSettingsObj(gpu.RawNamesForbidden, "nvidia.com/A100,nvidia.com/A400"),
			unreadable: true,
			want:       []string{"gn1.xlarge", "hd1.large", "rdma1.large", "u1.medium"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := slices.Clone(types)
			if tc.settings != nil {
				objs = append(objs, tc.settings)
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...)
			if tc.unreadable {
				dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("simulated API outage")
				})
			}
			items, err := DefaultProviders(dyn)["instancetype"](context.Background(), "tenant-foo")
			if err != nil {
				t.Fatalf("instancetype provider: %v", err)
			}
			if got := values(items); !slices.Equal(got, tc.want) {
				t.Fatalf("instancetype: got %v, want %v", got, tc.want)
			}
		})
	}
}
