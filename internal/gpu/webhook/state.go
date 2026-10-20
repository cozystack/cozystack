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

package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

var (
	vmiGVK      = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}
	kubeVirtGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "KubeVirt"}
)

const (
	// The KubeVirt CR the platform renders permittedHostDevices into.
	kubeVirtNamespace = "cozy-kubevirt"
	kubeVirtName      = "kubevirt"

	// labelMode is the GPU mode a Kairos pool's chart sets on its worker
	// VMI template.
	labelMode = "gpu.cozystack.io/mode"

	rootTenantNamespace = "tenant-root"
)

func (h *Handler) classes(ctx context.Context) (map[string]*gpuv1alpha1.GPUClass, error) {
	l := &gpuv1alpha1.GPUClassList{}
	if err := h.Client.List(ctx, l); err != nil {
		return nil, fmt.Errorf("list GPUClasses: %w", err)
	}
	out := make(map[string]*gpuv1alpha1.GPUClass, len(l.Items))
	for i := range l.Items {
		out[l.Items[i].Name] = &l.Items[i]
	}
	return out, nil
}

func resourceName(c *gpuv1alpha1.GPUClass) string {
	if dp := c.Spec.Source.DevicePlugin; dp != nil {
		return dp.ResourceName
	}
	return ""
}

// gpuNames is the effective GPU resource-name set.
func (h *Handler) gpuNames(ctx context.Context, classes map[string]*gpuv1alpha1.GPUClass) (sets.Set[string], error) {
	nodes := &gpuv1alpha1.GPUNodeList{}
	if err := h.Client.List(ctx, nodes); err != nil {
		return nil, fmt.Errorf("list GPUNodes: %w", err)
	}
	list := make([]gpuv1alpha1.GPUClass, 0, len(classes))
	for _, c := range classes {
		list = append(list, *c)
	}
	return sets.New(model.ResourceNames(list, h.Config.ResourceNames, nodes.Items)...), nil
}

// permitted returns the resource names of the KubeVirt permittedHostDevices
// table, or nil when the table is unset and KubeVirt checks nothing
// (renderresources.go validatePermittedHostDevices in v1.9.0). It reads
// the lists the controller's NotInPermittedHostDevices condition reads, so
// the class condition and this refusal agree.
func (h *Handler) permitted(ctx context.Context) (sets.Set[string], error) {
	kv := &unstructured.Unstructured{}
	kv.SetGroupVersionKind(kubeVirtGVK)
	err := h.Cache.Get(ctx, client.ObjectKey{Namespace: kubeVirtNamespace, Name: kubeVirtName}, kv)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get KubeVirt %s/%s: %w", kubeVirtNamespace, kubeVirtName, err)
	}
	table, ok, _ := unstructured.NestedMap(kv.Object, "spec", "configuration", "permittedHostDevices")
	if !ok || table == nil {
		return nil, nil
	}
	out := sets.New[string]()
	for _, list := range []string{"pciHostDevices", "mediatedDevices"} {
		entries, _, _ := unstructured.NestedSlice(table, list)
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok {
				if name, ok := m["resourceName"].(string); ok {
					out.Insert(name)
				}
			}
		}
	}
	return out, nil
}

// tenantReleaseKey returns the HelmRelease of the tenant that owns ns,
// the inverse of the tenant namespace naming in cozystack-api
// (computeTenantNamespace), or "" for a namespace no tenant owns.
func tenantReleaseKey(ns string) (namespace, name string) {
	switch parent := model.ParentNamespace(ns); {
	case ns == rootTenantNamespace:
		return rootTenantNamespace, rootTenantNamespace
	case parent == "":
		return "", ""
	case parent == rootTenantNamespace:
		return rootTenantNamespace, ns
	default:
		return parent, "tenant-" + strings.TrimPrefix(ns, parent+"-")
	}
}

// quotas reads the declared resourceQuotas of ns and its ancestors from
// their tenant HelmReleases, which keep GPU keys the namespace
// ResourceQuota never renders. Budgets are read past the cache, so a
// replica that is not the leader needs no HelmRelease watch.
func (h *Handler) quotas(ctx context.Context, ns string) (map[string]corev1.ResourceList, error) {
	out := map[string]corev1.ResourceList{}
	for _, a := range model.Ancestors(ns) {
		hrNamespace, hrName := tenantReleaseKey(a)
		if hrName == "" {
			continue
		}
		hr := &helmv2.HelmRelease{}
		err := h.Reader.Get(ctx, client.ObjectKey{Namespace: hrNamespace, Name: hrName}, hr)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get tenant HelmRelease %s/%s: %w", hrNamespace, hrName, err)
		}
		if hr.Spec.Values == nil {
			continue
		}
		var values struct {
			ResourceQuotas map[string]resource.Quantity `json:"resourceQuotas"`
		}
		if err := json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
			return nil, fmt.Errorf("decode values of tenant %s/%s: %w", hrNamespace, hrName, err)
		}
		rl := corev1.ResourceList{}
		for k, v := range values.ResourceQuotas {
			rl[corev1.ResourceName(k)] = v
		}
		out[a] = rl
	}
	return out, nil
}

func (h *Handler) entitled(ctx context.Context, ns string) (bool, error) {
	n := &corev1.Namespace{}
	err := h.Client.Get(ctx, client.ObjectKey{Name: ns}, n)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get namespace %s: %w", ns, err)
	}
	return model.Entitled(n.Labels), nil
}

// snapshot is the budget view of tenant ns: every device, the declared
// quotas of ns and its ancestors, and, where an ancestor carries a raw
// GPU key, the raw-name pods of that ancestor's subtree.
func (h *Handler) snapshot(ctx context.Context, ns string, classes map[string]*gpuv1alpha1.GPUClass) (*model.Snapshot, error) {
	s := &model.Snapshot{Classes: map[string]model.Class{}}
	for name, c := range classes {
		s.Classes[name] = model.ClassFrom(c)
	}
	devices := &gpuv1alpha1.GPUDeviceList{}
	if err := h.Client.List(ctx, devices); err != nil {
		return nil, fmt.Errorf("list GPUDevices: %w", err)
	}
	for i := range devices.Items {
		s.Devices = append(s.Devices, model.DeviceFrom(&devices.Items[i]))
	}
	var err error
	if s.Quotas, err = h.quotas(ctx, ns); err != nil {
		return nil, err
	}
	if s.Pods, err = h.rawPods(ctx, s, devices.Items); err != nil {
		return nil, err
	}
	return s, nil
}

// rawPods lists, past the cache, the pods of each subtree whose root
// carries a raw GPU key: no replica caches every pod, and only such a key
// needs them.
func (h *Handler) rawPods(ctx context.Context, s *model.Snapshot, devices []gpuv1alpha1.GPUDevice) ([]model.Pod, error) {
	names := sets.New[corev1.ResourceName]()
	for _, c := range s.Classes {
		if c.ResourceName != "" {
			names.Insert(c.ResourceName)
		}
	}
	var roots []string
	for a, q := range s.Quotas {
		for n := range names {
			if _, ok := model.RawQuotaLimit(q, n); ok {
				roots = append(roots, a)
				break
			}
		}
	}
	if len(roots) == 0 {
		return nil, nil
	}
	namespaces := &corev1.NamespaceList{}
	if err := h.Client.List(ctx, namespaces); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	covered := map[client.ObjectKey]map[corev1.ResourceName]int64{}
	for i := range devices {
		d := &devices[i]
		c, ok := s.Classes[d.Spec.ClassName]
		if d.Status.AttachedTo == nil || !ok {
			continue
		}
		k := client.ObjectKey{Namespace: d.Namespace, Name: d.Status.AttachedTo.VMI}
		if covered[k] == nil {
			covered[k] = map[corev1.ResourceName]int64{}
		}
		covered[k][c.ResourceName] += int64(d.Spec.Count)
	}
	var out []model.Pod
	for _, n := range namespaces.Items {
		inSubtree := false
		for _, r := range roots {
			inSubtree = inSubtree || model.InSubtree(n.Name, r)
		}
		if !inSubtree {
			continue
		}
		pods := &corev1.PodList{}
		if err := h.Reader.List(ctx, pods, client.InNamespace(n.Name)); err != nil {
			return nil, fmt.Errorf("list pods in %s: %w", n.Name, err)
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			var cov map[corev1.ResourceName]int64
			for _, ref := range p.OwnerReferences {
				if ref.Kind == vmiGVK.Kind {
					cov = covered[client.ObjectKey{Namespace: p.Namespace, Name: ref.Name}]
				}
			}
			if mp, ok := model.PodFrom(p, names, cov); ok {
				out = append(out, mp)
			}
		}
	}
	return out, nil
}
