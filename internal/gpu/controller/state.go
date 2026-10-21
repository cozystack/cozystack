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

package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const (
	tenantKind = "Tenant"
)

// world is one pass's view of everything placement and lifecycle read.
type world struct {
	now      time.Time
	classes  map[string]*gpuv1alpha1.GPUClass
	devices  []*gpuv1alpha1.GPUDevice
	nodes    map[string]*corev1.Node
	gpuNodes map[string]*gpuv1alpha1.GPUNode
	entitled map[string]bool
	// quotaErr is set when tenant budgets could not be read; orders then wait.
	quotaErr error

	// The KubeVirt view, filled only with the feature enabled.
	vmis map[types.NamespacedName]*vmi
	vms  map[types.NamespacedName]*vm
	// permitted is the KubeVirt permittedHostDevices table, nil when unset.
	permitted sets.Set[string]

	resourceNames []string
	// lost holds the nodes that have lost capacity past the grace period.
	lost map[string]*nodeLoss

	snap *model.Snapshot
	// aborted is set once a device write fails. The snapshot then holds a
	// placement the cluster does not, so the pass places nothing more and
	// the next pass starts from a fresh read.
	aborted bool
}

type nodeLoss struct {
	notReady bool
	// healthy holds, for each resource the node holds more units of than it
	// serves, the units it still serves.
	healthy map[corev1.ResourceName]int64
}

func (l *nodeLoss) covers(name corev1.ResourceName) bool {
	if l == nil {
		return false
	}
	if l.notReady {
		return true
	}
	_, ok := l.healthy[name]
	return ok
}

func key(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

func (r *Reconciler) load(ctx context.Context) (*world, error) {
	w := &world{
		now:      r.now(),
		classes:  map[string]*gpuv1alpha1.GPUClass{},
		nodes:    map[string]*corev1.Node{},
		gpuNodes: map[string]*gpuv1alpha1.GPUNode{},
		entitled: map[string]bool{},
		vmis:     map[types.NamespacedName]*vmi{},
		vms:      map[types.NamespacedName]*vm{},
		lost:     map[string]*nodeLoss{},
	}

	classes := &gpuv1alpha1.GPUClassList{}
	if err := r.Client.List(ctx, classes); err != nil {
		return nil, fmt.Errorf("list GPUClasses: %w", err)
	}
	for i := range classes.Items {
		w.classes[classes.Items[i].Name] = &classes.Items[i]
	}

	// Devices are read past the cache: the binder's own writes from the last
	// pass must be visible, or a device bound a moment ago would look free
	// and its units would be booked twice.
	devices := &gpuv1alpha1.GPUDeviceList{}
	if err := r.Reader.List(ctx, devices); err != nil {
		return nil, fmt.Errorf("list GPUDevices: %w", err)
	}
	for i := range devices.Items {
		w.devices = append(w.devices, &devices.Items[i])
	}
	sort.Slice(w.devices, func(i, j int) bool {
		a, b := w.devices[i], w.devices[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
	})

	if !r.Config.Enabled {
		return w, nil
	}

	nodes := &corev1.NodeList{}
	if err := r.Client.List(ctx, nodes); err != nil {
		return nil, fmt.Errorf("list Nodes: %w", err)
	}
	for i := range nodes.Items {
		w.nodes[nodes.Items[i].Name] = &nodes.Items[i]
	}
	gpuNodes := &gpuv1alpha1.GPUNodeList{}
	if err := r.Client.List(ctx, gpuNodes); err != nil {
		return nil, fmt.Errorf("list GPUNodes: %w", err)
	}
	for i := range gpuNodes.Items {
		w.gpuNodes[gpuNodes.Items[i].Name] = &gpuNodes.Items[i]
	}
	namespaces := &corev1.NamespaceList{}
	if err := r.Client.List(ctx, namespaces); err != nil {
		return nil, fmt.Errorf("list Namespaces: %w", err)
	}
	for i := range namespaces.Items {
		w.entitled[namespaces.Items[i].Name] = model.Entitled(namespaces.Items[i].Labels)
	}

	for _, gvk := range []struct {
		list func(*unstructured.Unstructured)
		kind string
	}{
		{func(u *unstructured.Unstructured) { v := vmiFrom(u); w.vmis[key(v.Namespace, v.Name)] = v }, vmiGVK.Kind},
		{func(u *unstructured.Unstructured) { v := vmFrom(u); w.vms[key(v.Namespace, v.Name)] = v }, vmGVK.Kind},
	} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(vmiGVK.GroupVersion().WithKind(gvk.kind + "List"))
		if err := r.kubeVirtReader().List(ctx, list); err != nil {
			return nil, fmt.Errorf("list %s: %w", gvk.kind, err)
		}
		for i := range list.Items {
			gvk.list(&list.Items[i])
		}
	}
	kvs := &unstructured.UnstructuredList{}
	kvs.SetGroupVersionKind(kubeVirtGVK.GroupVersion().WithKind(kubeVirtGVK.Kind + "List"))
	if err := r.kubeVirtReader().List(ctx, kvs); err != nil {
		return nil, fmt.Errorf("list KubeVirt: %w", err)
	}
	for i := range kvs.Items {
		if kvs.Items[i].GetNamespace() == model.KubeVirtNamespace && kvs.Items[i].GetName() == model.KubeVirtName {
			w.permitted = model.PermittedResourceNames(&kvs.Items[i])
		}
	}

	classList := make([]gpuv1alpha1.GPUClass, 0, len(w.classes))
	for _, c := range w.classes {
		classList = append(classList, *c)
	}
	nodeList := make([]gpuv1alpha1.GPUNode, 0, len(w.gpuNodes))
	for _, n := range w.gpuNodes {
		nodeList = append(nodeList, *n)
	}
	w.resourceNames = model.ResourceNames(classList, r.Config.ResourceNames, nodeList)

	quotas, err := r.tenantQuotas(ctx)
	if err != nil {
		w.quotaErr = err
	}

	pods := &corev1.PodList{}
	if err := r.Client.List(ctx, pods); err != nil {
		return nil, fmt.Errorf("list Pods: %w", err)
	}

	r.detectLostCapacity(w)
	w.snap = w.buildSnapshot(quotas, pods.Items)
	return w, nil
}

func (r *Reconciler) kubeVirtReader() client.Reader {
	if r.Cache != nil {
		return r.Cache
	}
	return r.Client
}

// tenantQuotas reads each tenant's declared resourceQuotas, keys as written,
// from the tenant HelmReleases, the same source cozystack-api's quota gate
// reads: GPU keys are never rendered into a namespace ResourceQuota.
func (r *Reconciler) tenantQuotas(ctx context.Context) (map[string]corev1.ResourceList, error) {
	releases := &helmv2.HelmReleaseList{}
	if err := r.Client.List(ctx, releases, client.MatchingLabels{appsv1alpha1.ApplicationKindLabel: tenantKind}); err != nil {
		return nil, fmt.Errorf("list tenant HelmReleases: %w", err)
	}
	out := map[string]corev1.ResourceList{}
	for i := range releases.Items {
		hr := &releases.Items[i]
		if hr.Spec.Values == nil {
			continue
		}
		rl, err := model.DeclaredQuotas(hr.Spec.Values.Raw)
		if err != nil {
			return nil, fmt.Errorf("tenant %s/%s: %w", hr.Namespace, hr.Name, err)
		}
		out[model.TenantNamespaceOf(hr.Namespace, hr.Name, model.TenantPrefix)] = rl
	}
	return out, nil
}

// detectLostCapacity marks nodes that have been NotReady, or that serve
// fewer units of a resource than they hold, for longer than
// LostCapacityGrace; the grace outlasts a kubelet restart, after which
// kubelet reports zero until the plugin registers again.
//
// model.ServedUnits counts what a node serves. A held name no agent
// function carries, because the node has no GPUNode, because the agent sees
// its devices only by NVML UUID, or because kubelet dropped it when its
// last device went bad, is judged by the node's allocatable count.
func (r *Reconciler) detectLostCapacity(w *world) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	grace := r.Config.LostCapacityGrace
	for name, n := range w.nodes {
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status != corev1.ConditionTrue && w.now.Sub(c.LastTransitionTime.Time) >= grace {
				w.lost[name] = &nodeLoss{notReady: true}
			}
		}
		if w.lost[name] != nil {
			continue
		}
		var devices []gpuv1alpha1.GPUNodeDevice
		if gn := w.gpuNodes[name]; gn != nil {
			devices = gn.Status.Devices
		}
		held := map[corev1.ResourceName]int64{}
		for _, d := range w.devices {
			if d.Status.NodeName == name && model.HoldsGPUs(d.Status.Phase) {
				if c := w.classes[d.Spec.ClassName]; c != nil && c.Spec.Source.DevicePlugin != nil {
					held[corev1.ResourceName(c.Spec.Source.DevicePlugin.ResourceName)] += int64(d.Spec.Count)
				}
			}
		}
		for rn, units := range held {
			served := model.ServedUnits(n, devices, rn)
			if units <= served {
				continue
			}
			k := rn.String() + "@" + name
			seen[k] = true
			since, ok := r.degradedSince[k]
			if !ok {
				since = w.now
				r.degradedSince[k] = since
			}
			if w.now.Sub(since) >= grace {
				if w.lost[name] == nil {
					w.lost[name] = &nodeLoss{healthy: map[corev1.ResourceName]int64{}}
				}
				w.lost[name].healthy[rn] = served
			}
		}
	}
	for k := range r.degradedSince {
		if !seen[k] {
			delete(r.degradedSince, k)
		}
	}
}

// buildSnapshot projects the world for the shared model. A lost node's
// allocatable is capped at what it can still serve.
func (w *world) buildSnapshot(quotas map[string]corev1.ResourceList, pods []corev1.Pod) *model.Snapshot {
	s := &model.Snapshot{Classes: map[string]model.Class{}, Quotas: quotas}
	for name, c := range w.classes {
		s.Classes[name] = model.ClassFrom(c)
	}
	for _, d := range w.devices {
		s.Devices = append(s.Devices, model.DeviceFrom(d))
	}
	for _, n := range w.nodes {
		mn := model.NodeFrom(n, nil)
		if l := w.lost[n.Name]; l != nil {
			for rn, h := range l.healthy {
				mn.Allocatable[rn] = min(mn.Allocatable[rn], h)
			}
		}
		s.Nodes = append(s.Nodes, mn)
	}
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].Name < s.Nodes[j].Name })

	names := sets.New[corev1.ResourceName]()
	for _, n := range w.resourceNames {
		names.Insert(corev1.ResourceName(n))
	}
	// A Lost device covers its VMI's pod for budgets, which already count
	// the device, but not for node capacity: it holds no units, so the pod
	// still running on its node holds them itself.
	coveredForBudget := model.CoveredForBudget(w.devices, s.Classes)
	coveredForCapacity := model.CoveredForCapacity(w.devices, s.Classes)
	for i := range pods {
		p := &pods[i]
		vmi, _, _ := model.LauncherVMI(p, func(k types.NamespacedName) (sets.Set[types.UID], error) {
			if v := w.vmis[k]; v != nil {
				return v.ActivePods, nil
			}
			return nil, nil
		})
		budget, okBudget := model.PodFrom(p, names, coveredForBudget[vmi])
		if okBudget {
			s.Pods = append(s.Pods, budget)
		}
		capacity, ok := model.PodFrom(p, names, coveredForCapacity[vmi])
		if !ok {
			continue
		}
		extra := map[corev1.ResourceName]int64{}
		for rn, units := range capacity.Units {
			if left := units - budget.Units[rn]; left > 0 {
				extra[rn] = left
			}
		}
		if len(extra) > 0 {
			// The empty namespace is in no tenant's subtree, so these units
			// count against the node's capacity and against no budget: the
			// budget already counts them through the Lost device.
			s.Pods = append(s.Pods, model.Pod{NodeName: p.Spec.NodeName, Units: extra})
		}
	}
	return s
}

// setDevice mirrors a placement change into the snapshot, so later
// decisions in the same pass see it.
func (w *world) setDevice(d *gpuv1alpha1.GPUDevice) {
	for i := range w.snap.Devices {
		if w.snap.Devices[i].Namespace == d.Namespace && w.snap.Devices[i].Name == d.Name {
			w.snap.Devices[i] = model.DeviceFrom(d)
			return
		}
	}
}

func (w *world) resourceName(class string) corev1.ResourceName {
	if c := w.classes[class]; c != nil && c.Spec.Source.DevicePlugin != nil {
		return corev1.ResourceName(c.Spec.Source.DevicePlugin.ResourceName)
	}
	return ""
}

func (w *world) device(namespace, name string) *gpuv1alpha1.GPUDevice {
	for _, d := range w.devices {
		if d.Namespace == namespace && d.Name == name {
			return d
		}
	}
	return nil
}

func isTemplate(d *gpuv1alpha1.GPUDevice) bool {
	_, ok := d.Labels[gpu.LabelTemplateDevice]
	return ok
}
