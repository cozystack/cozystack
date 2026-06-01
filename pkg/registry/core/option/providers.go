// SPDX-License-Identifier: Apache-2.0
// Providers compute the option items for each named dropdown source. They run
// with the apiserver's privileged client (dynamic.Interface), so tenants need
// no direct access to Nodes, the KubeVirt CR, cluster instancetypes, etc.

package option

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// kubevirtNamespace is where the singleton KubeVirt CR lives.
const kubevirtNamespace = "cozy-kubevirt"

// publicImagesNamespace and imagePVCPrefix locate the default VM image catalog.
const (
	publicImagesNamespace = "cozy-public"
	imagePVCPrefix        = "vm-default-images-"
)

// providerFunc computes the items for one source. namespace is the request
// namespace (empty for cluster-scoped callers); cluster-global providers ignore
// it.
type providerFunc func(ctx context.Context, namespace string) ([]corev1alpha1.OptionItem, error)

var (
	gvrNodes        = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}
	gvrPVCs         = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}
	gvrKubevirts    = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "kubevirts"}
	gvrInstancetype = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclusterinstancetypes"}
	gvrPreference   = schema.GroupVersionResource{Group: "instancetype.kubevirt.io", Version: "v1beta1", Resource: "virtualmachineclusterpreferences"}
	gvrNADs         = schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}
	gvrHelmReleases = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	gvrStorageClass = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
	gvrBackupClass  = schema.GroupVersionResource{Group: "backups.cozystack.io", Version: "v1alpha1", Resource: "backupclasses"}
	gvrVMDisks      = schema.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: "vmdisks"}
	gvrPlans        = schema.GroupVersionResource{Group: "backups.cozystack.io", Version: "v1alpha1", Resource: "plans"}
	gvrBackups      = schema.GroupVersionResource{Group: "backups.cozystack.io", Version: "v1alpha1", Resource: "backups"}
	gvrAppDefs      = schema.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: "applicationdefinitions"}
)

const defaultStorageClassAnnotation = "storageclass.kubernetes.io/is-default-class"

// DefaultProviders returns the registry of dropdown sources.
func DefaultProviders(dyn dynamic.Interface) map[string]providerFunc {
	return map[string]providerFunc{
		"gpu":             gpuProvider(dyn),
		"instancetype":    nameListProvider(dyn, gvrInstancetype),
		"instanceprofile": nameListProvider(dyn, gvrPreference),
		"network":         nameListNamespacedProvider(dyn, gvrNADs),
		"image":           imageProvider(dyn),
		"storagepool":     storagePoolProvider(dyn),
		"storageclass":    storageClassProvider(dyn),
		"backupclass":     nameListProvider(dyn, gvrBackupClass),
		"vmdisk":          vmDiskProvider(dyn),
		"plan":            nameListNamespacedProvider(dyn, gvrPlans),
		"backup":          nameListNamespacedProvider(dyn, gvrBackups),
		"appkind":         appKindProvider(dyn),
	}
}

// appKindProvider lists the application kinds known to the cluster from
// ApplicationDefinitions (apps.cozystack.io). These are the kinds selectable
// in the backup forms' applicationRef.kind / targetApplicationRef.kind fields.
func appKindProvider(dyn dynamic.Interface) providerFunc {
	return func(ctx context.Context, _ string) ([]corev1alpha1.OptionItem, error) {
		list, err := dyn.Resource(gvrAppDefs).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		items := make([]corev1alpha1.OptionItem, 0, len(list.Items))
		seen := map[string]struct{}{}
		for i := range list.Items {
			kind, ok, _ := unstructured.NestedString(list.Items[i].Object, "spec", "application", "kind")
			if !ok || kind == "" {
				continue
			}
			if _, dup := seen[kind]; dup {
				continue
			}
			seen[kind] = struct{}{}
			items = append(items, corev1alpha1.OptionItem{Value: kind})
		}
		sortItems(items)
		return items, nil
	}
}

// storageClassProvider lists StorageClasses and marks the cluster-default one
// (annotated storageclass.kubernetes.io/is-default-class) so the UI can
// preselect it.
func storageClassProvider(dyn dynamic.Interface) providerFunc {
	return func(ctx context.Context, _ string) ([]corev1alpha1.OptionItem, error) {
		list, err := dyn.Resource(gvrStorageClass).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		items := make([]corev1alpha1.OptionItem, 0, len(list.Items))
		for i := range list.Items {
			name := list.Items[i].GetName()
			isDefault := list.Items[i].GetAnnotations()[defaultStorageClassAnnotation] == "true"
			item := corev1alpha1.OptionItem{Value: name, Default: isDefault}
			if isDefault {
				item.Label = name + " (default)"
			}
			items = append(items, item)
		}
		sortItems(items)
		return items, nil
	}
}

// vmDiskProvider lists VMDisk apps in the request namespace and shows the disk
// size in the label (mirrors the old VMDiskWidget).
func vmDiskProvider(dyn dynamic.Interface) providerFunc {
	return func(ctx context.Context, namespace string) ([]corev1alpha1.OptionItem, error) {
		if namespace == "" {
			return nil, nil
		}
		list, err := dyn.Resource(gvrVMDisks).Namespace(namespace).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		items := make([]corev1alpha1.OptionItem, 0, len(list.Items))
		for i := range list.Items {
			name := list.Items[i].GetName()
			item := corev1alpha1.OptionItem{Value: name}
			if storage, ok, _ := unstructured.NestedString(list.Items[i].Object, "spec", "storage"); ok && storage != "" {
				item.Label = fmt.Sprintf("%s (%s)", name, storage)
			}
			items = append(items, item)
		}
		sortItems(items)
		return items, nil
	}
}

// nameListProvider lists a cluster-scoped resource and emits one option per
// object name.
func nameListProvider(dyn dynamic.Interface, gvr schema.GroupVersionResource) providerFunc {
	return func(ctx context.Context, _ string) ([]corev1alpha1.OptionItem, error) {
		list, err := dyn.Resource(gvr).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		return itemsFromNames(list), nil
	}
}

// nameListNamespacedProvider lists a namespaced resource in the request
// namespace and emits one option per object name.
func nameListNamespacedProvider(dyn dynamic.Interface, gvr schema.GroupVersionResource) providerFunc {
	return func(ctx context.Context, namespace string) ([]corev1alpha1.OptionItem, error) {
		if namespace == "" {
			return nil, nil
		}
		list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		return itemsFromNames(list), nil
	}
}

// gpuProvider intersects the KubeVirt permittedHostDevices whitelist with what
// the nodes actually advertise as allocatable.
func gpuProvider(dyn dynamic.Interface) providerFunc {
	return func(ctx context.Context, _ string) ([]corev1alpha1.OptionItem, error) {
		kvList, err := dyn.Resource(gvrKubevirts).Namespace(kubevirtNamespace).List(ctx, listOpts())
		if err != nil {
			return nil, fmt.Errorf("list kubevirts: %w", err)
		}
		if len(kvList.Items) == 0 {
			return nil, nil
		}
		whitelist := permittedResourceNames(&kvList.Items[0])
		if len(whitelist) == 0 {
			return nil, nil
		}

		nodeList, err := dyn.Resource(gvrNodes).List(ctx, listOpts())
		if err != nil {
			return nil, fmt.Errorf("list nodes: %w", err)
		}

		// resourceName -> total count, and -> nodes that have it.
		total := map[string]int64{}
		nodesByRes := map[string][]string{}
		for i := range nodeList.Items {
			node := &nodeList.Items[i]
			alloc, _, _ := unstructured.NestedMap(node.Object, "status", "allocatable")
			for res := range whitelist {
				raw, ok := alloc[res]
				if !ok {
					continue
				}
				qty, err := resource.ParseQuantity(fmt.Sprintf("%v", raw))
				if err != nil {
					continue
				}
				if n := qty.Value(); n > 0 {
					total[res] += n
					nodesByRes[res] = append(nodesByRes[res], node.GetName())
				}
			}
		}

		items := make([]corev1alpha1.OptionItem, 0, len(whitelist))
		for res := range whitelist {
			desc := "not currently available on any node"
			if n := total[res]; n > 0 {
				sort.Strings(nodesByRes[res])
				desc = fmt.Sprintf("%d available on %s", n, strings.Join(nodesByRes[res], ", "))
			}
			items = append(items, corev1alpha1.OptionItem{Value: res, Description: desc})
		}
		sortItems(items)
		return items, nil
	}
}

// permittedResourceNames pulls resourceNames from pciHostDevices and
// mediatedDevices of a KubeVirt CR.
func permittedResourceNames(kv *unstructured.Unstructured) map[string]struct{} {
	out := map[string]struct{}{}
	for _, group := range []string{"pciHostDevices", "mediatedDevices"} {
		devices, _, _ := unstructured.NestedSlice(kv.Object, "spec", "configuration", "permittedHostDevices", group)
		for _, d := range devices {
			dm, ok := d.(map[string]interface{})
			if !ok {
				continue
			}
			if name, ok := dm["resourceName"].(string); ok && name != "" {
				out[name] = struct{}{}
			}
		}
	}
	return out
}

// imageProvider lists the default image PVCs in cozy-public and strips the
// vm-default-images- prefix to get the catalog name used by the vm-disk chart.
func imageProvider(dyn dynamic.Interface) providerFunc {
	return func(ctx context.Context, _ string) ([]corev1alpha1.OptionItem, error) {
		list, err := dyn.Resource(gvrPVCs).Namespace(publicImagesNamespace).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		items := make([]corev1alpha1.OptionItem, 0, len(list.Items))
		for i := range list.Items {
			name := list.Items[i].GetName()
			if !strings.HasPrefix(name, imagePVCPrefix) {
				continue
			}
			items = append(items, corev1alpha1.OptionItem{Value: strings.TrimPrefix(name, imagePVCPrefix)})
		}
		sortItems(items)
		return items, nil
	}
}

// storagePoolProvider derives selectable pool names from the seaweedfs
// HelmRelease(s) in the request namespace (volume.pools and
// volume.zones.<zone>.pools map keys). Best-effort: returns nil when no
// seaweedfs release is present, and the UI falls back to free text.
func storagePoolProvider(dyn dynamic.Interface) providerFunc {
	return func(ctx context.Context, namespace string) ([]corev1alpha1.OptionItem, error) {
		if namespace == "" {
			return nil, nil
		}
		list, err := dyn.Resource(gvrHelmReleases).Namespace(namespace).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		pools := map[string]struct{}{}
		for i := range list.Items {
			values, ok, _ := unstructured.NestedMap(list.Items[i].Object, "spec", "values")
			if !ok {
				continue
			}
			volume, ok := values["volume"].(map[string]interface{})
			if !ok {
				continue
			}
			collectPoolKeys(volume["pools"], pools)
			if zones, ok := volume["zones"].(map[string]interface{}); ok {
				for _, z := range zones {
					if zm, ok := z.(map[string]interface{}); ok {
						collectPoolKeys(zm["pools"], pools)
					}
				}
			}
		}
		items := make([]corev1alpha1.OptionItem, 0, len(pools))
		for p := range pools {
			items = append(items, corev1alpha1.OptionItem{Value: p})
		}
		sortItems(items)
		return items, nil
	}
}

func collectPoolKeys(v interface{}, out map[string]struct{}) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return
	}
	for k := range m {
		out[k] = struct{}{}
	}
}

// itemsFromNames builds a sorted option list from object names.
func itemsFromNames(list *unstructured.UnstructuredList) []corev1alpha1.OptionItem {
	items := make([]corev1alpha1.OptionItem, 0, len(list.Items))
	for i := range list.Items {
		items = append(items, corev1alpha1.OptionItem{Value: list.Items[i].GetName()})
	}
	sortItems(items)
	return items
}

func sortItems(items []corev1alpha1.OptionItem) {
	sort.Slice(items, func(i, j int) bool { return items[i].Value < items[j].Value })
}

func logProviderError(source string, err error) {
	klog.V(2).InfoS("option provider failed", "source", source, "err", err)
}
