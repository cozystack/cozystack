// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"

	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// rawNamesForbidden reports whether the hoster has set gpu.rawResourceNames
// to forbidden, and the effective GPU resource-name set, from the settings
// ConfigMap the controller writes. Without the ConfigMap raw names are
// allowed.
func rawNamesForbidden(ctx context.Context, dyn dynamic.Interface) (bool, sets.Set[string], error) {
	cm, err := dyn.Resource(gvrConfigMaps).Namespace(gpu.SettingsNamespace).Get(ctx, gpu.SettingsConfigMap, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	if gpu.RawNamesPolicy(data[gpu.SettingsKeyRawResourceNames]) != gpu.RawNamesForbidden {
		return false, nil, nil
	}
	return true, sets.New(model.ParseResourceNames(data[gpu.SettingsKeyResourceNames])...), nil
}

// rawGPUProvider serves the raw-name gpu source only while raw names are
// allowed. The policy check is advisory, like the image filter: admission
// enforces forbidden, so an unreadable setting serves the names rather than
// emptying the picker.
func rawGPUProvider(dyn dynamic.Interface) providerFunc {
	raw := gpuProvider(dyn)
	return func(ctx context.Context, namespace string) ([]corev1alpha1.OptionItem, error) {
		forbidden, _, err := rawNamesForbidden(ctx, dyn)
		if err != nil {
			logProviderError("gpu", err)
		}
		if forbidden {
			return nil, nil
		}
		return raw(ctx, namespace)
	}
}

// instancetypeProvider lists cluster instance types and, while raw GPU names
// are forbidden, leaves out those that carry GPU entries: any gpus entry, or
// a hostDevices entry on a GPU resource name.
func instancetypeProvider(dyn dynamic.Interface) providerFunc {
	return func(ctx context.Context, _ string) ([]corev1alpha1.OptionItem, error) {
		list, err := dyn.Resource(gvrInstancetype).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		forbidden, names, err := rawNamesForbidden(ctx, dyn)
		if err != nil {
			logProviderError("instancetype", err)
		}
		if forbidden {
			kept := list.Items[:0]
			for _, it := range list.Items {
				if !hasGPUEntries(&it, names) {
					kept = append(kept, it)
				}
			}
			list.Items = kept
		}
		return itemsFromNames(list), nil
	}
}

func hasGPUEntries(it *unstructured.Unstructured, names sets.Set[string]) bool {
	if gpus, _, _ := unstructured.NestedSlice(it.Object, "spec", "gpus"); len(gpus) > 0 {
		return true
	}
	devices, _, _ := unstructured.NestedSlice(it.Object, "spec", "hostDevices")
	for _, d := range devices {
		if dm, ok := d.(map[string]any); ok {
			if name, _ := dm["deviceName"].(string); names.Has(name) {
				return true
			}
		}
	}
	return false
}
