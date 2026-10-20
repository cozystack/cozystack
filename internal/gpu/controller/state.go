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
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
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
	rootTenantNamespace   = "tenant-root"
	tenantNamespacePrefix = "tenant-"
	tenantKind            = "Tenant"
)

// world is one pass's view of everything placement and lifecycle read.
type world struct {
	now      time.Time
	classes  map[string]*gpuv1alpha1.GPUClass
	claims   []*gpuv1alpha1.GPUClaim
	nodes    map[string]*corev1.Node
	gpuNodes map[string]*gpuv1alpha1.GPUNode
	entitled map[string]bool
	// quotaErr is set when tenant budgets could not be listed; every order
	// then waits. badQuotas holds the tenants whose values do not parse:
	// orders in their subtrees wait, and nobody else's.
	quotaErr  error
	badQuotas map[string]error

	// The KubeVirt view, filled only with the feature enabled.
	vmis map[types.NamespacedName]*vmi
	vms  map[types.NamespacedName]*vm
	// permitted is the KubeVirt permittedHostDevices table, nil when unset.
	permitted sets.Set[string]
	// launchers are the pods each VMI lists in status.activePods. gated
	// holds the VMIs with a pod at gpu.SchedulingGateClaims, pinned those
	// whose pods have all left it.
	launchers map[types.NamespacedName][]*corev1.Pod
	gated     map[types.NamespacedName]bool
	pinned    map[types.NamespacedName]bool

	resourceNames []string
	// lost holds the nodes that have lost capacity past the grace period.
	lost map[string]*nodeLoss

	snap *model.Snapshot
	// aborted is set once a claim write fails. The snapshot then holds a
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

		launchers: map[types.NamespacedName][]*corev1.Pod{},
		gated:     map[types.NamespacedName]bool{},
		pinned:    map[types.NamespacedName]bool{},
	}

	classes := &gpuv1alpha1.GPUClassList{}
	if err := r.Client.List(ctx, classes); err != nil {
		return nil, fmt.Errorf("list GPUClasses: %w", err)
	}
	for i := range classes.Items {
		w.classes[classes.Items[i].Name] = &classes.Items[i]
	}

	// Claims are read past the cache: the binder's own writes from the last
	// pass must be visible, or a claim bound a moment ago would look free
	// and its units would be booked twice.
	claims := &gpuv1alpha1.GPUClaimList{}
	if err := r.Reader.List(ctx, claims); err != nil {
		return nil, fmt.Errorf("list GPUClaims: %w", err)
	}
	for i := range claims.Items {
		w.claims = append(w.claims, &claims.Items[i])
	}
	sort.Slice(w.claims, func(i, j int) bool {
		a, b := w.claims[i], w.claims[j]
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
		if err := r.listKubeVirt(ctx, w, list); err != nil {
			return nil, fmt.Errorf("list %s: %w", gvk.kind, err)
		}
		for i := range list.Items {
			gvk.list(&list.Items[i])
		}
	}
	kvs := &unstructured.UnstructuredList{}
	kvs.SetGroupVersionKind(kubeVirtGVK.GroupVersion().WithKind(kubeVirtGVK.Kind + "List"))
	if err := r.listKubeVirt(ctx, w, kvs); err != nil {
		return nil, fmt.Errorf("list KubeVirt: %w", err)
	}
	var tableGPUs []string
	for i := range kvs.Items {
		if kvs.Items[i].GetNamespace() == kubeVirtNamespace && kvs.Items[i].GetName() == kubeVirtName {
			w.permitted = permittedResourceNames(&kvs.Items[i])
			tableGPUs = permittedGPUNames(&kvs.Items[i])
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
	w.resourceNames = model.ResourceNames(classList, append(slices.Clone(r.Config.ResourceNames), tableGPUs...), nodeList)

	quotas, bad, err := r.tenantQuotas(ctx)
	w.quotaErr, w.badQuotas = err, bad

	pods := &corev1.PodList{}
	if err := r.Client.List(ctx, pods); err != nil {
		return nil, fmt.Errorf("list Pods: %w", err)
	}

	for i := range pods.Items {
		p := &pods.Items[i]
		k, ok := w.launcherVMI(p)
		// A finished pod, such as KubeVirt's temporary pod once its volume
		// is bound, neither waits at the gate nor stands for a pinned one.
		if !ok || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		w.launchers[k] = append(w.launchers[k], p)
		if gated(p) {
			w.gated[k] = true
		}
	}
	for k := range w.launchers {
		w.pinned[k] = !w.gated[k]
	}

	r.detectLostCapacity(w)
	w.snap = w.buildSnapshot(quotas, pods.Items)
	return w, nil
}

// launcherVMI returns the VMI that lists p in status.activePods.
func (w *world) launcherVMI(p *corev1.Pod) (types.NamespacedName, bool) {
	k, ok, _ := model.LauncherVMI(p, func(k types.NamespacedName) (sets.Set[types.UID], error) {
		if v := w.vmis[k]; v != nil {
			return v.ActivePods, nil
		}
		return nil, nil
	})
	return k, ok
}

// listKubeVirt lists a KubeVirt kind, and none while no CRD serves it: on
// a fresh install GPUs are enabled before KubeVirt's operator installs its
// CRDs, and finalizers and usage records must not wait for that. Once a
// claim is attached or a template claim exists, an unserved kind is a
// discovery gap, such as after an API server restart, not an absent VMI,
// and the pass fails rather than detach a running VM's claim or delete a
// template claim.
func (r *Reconciler) listKubeVirt(ctx context.Context, w *world, list *unstructured.UnstructuredList) error {
	err := r.kubeVirtReader().List(ctx, list)
	if !meta.IsNoMatchError(err) {
		return err
	}
	for _, d := range w.claims {
		if d.Status.AttachedTo != nil || isTemplate(d) {
			return fmt.Errorf("KubeVirt kinds are not served while GPUClaim %s/%s is attached or a template claim: %w", d.Namespace, d.Name, err)
		}
	}
	list.Items = nil
	return nil
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
func (r *Reconciler) tenantQuotas(ctx context.Context) (map[string]corev1.ResourceList, map[string]error, error) {
	releases := &helmv2.HelmReleaseList{}
	if err := r.Client.List(ctx, releases, client.MatchingLabels{appsv1alpha1.ApplicationKindLabel: tenantKind}); err != nil {
		return nil, nil, fmt.Errorf("list tenant HelmReleases: %w", err)
	}
	out := map[string]corev1.ResourceList{}
	bad := map[string]error{}
	for i := range releases.Items {
		hr := &releases.Items[i]
		if hr.Spec.Values == nil {
			continue
		}
		var values struct {
			ResourceQuotas map[string]resource.Quantity `json:"resourceQuotas"`
		}
		ns := tenantNamespace(hr.Namespace, strings.TrimPrefix(hr.Name, tenantNamespacePrefix))
		if err := json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
			bad[ns] = fmt.Errorf("decode values of tenant %s/%s: %w", hr.Namespace, hr.Name, err)
			continue
		}
		rl := corev1.ResourceList{}
		for k, v := range values.ResourceQuotas {
			rl[corev1.ResourceName(k)] = v
		}
		out[ns] = rl
	}
	return out, bad, nil
}

// budgetsUnreadable returns why the budgets an order in ns answers to
// cannot be read, or "": the listing failed, or ns or an ancestor has
// values that do not parse. Such an order waits rather than being judged
// without a limit that may apply to it.
func (w *world) budgetsUnreadable(ns string) string {
	if w.quotaErr != nil {
		return "tenant GPU budgets cannot be read: " + w.quotaErr.Error()
	}
	for _, a := range model.Ancestors(ns) {
		if err := w.badQuotas[a]; err != nil {
			return "the GPU budgets of tenant " + a + " cannot be read: " + err.Error()
		}
	}
	return ""
}

// tenantNamespace mirrors REST.computeTenantNamespace in cozystack-api.
func tenantNamespace(hrNamespace, app string) string {
	switch {
	case hrNamespace == rootTenantNamespace && app == "root":
		return rootTenantNamespace
	case hrNamespace == rootTenantNamespace:
		return tenantNamespacePrefix + app
	default:
		return hrNamespace + "-" + app
	}
}

// detectLostCapacity marks nodes that have been NotReady, or that serve
// fewer units of a resource than they hold, for longer than
// LostCapacityGrace; the grace outlasts a kubelet restart, after which
// kubelet reports zero until the plugin registers again.
//
// The units a node serves come from the agent's functions that carry the
// resource name, as kubelet's allocatable count for them: kubelet lists
// only the plugin devices that are healthy, so one bad mdev slot does not
// take its parent's other slots with it. A held name no function carries,
// because the node has no GPUNode, because the agent sees its devices only
// by NVML UUID, or because kubelet dropped it when its last device went
// bad, is judged by the node's allocatable count instead, which also caps
// the agent's count.
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
		serves := map[corev1.ResourceName]int64{}
		mapped := sets.New[corev1.ResourceName]()
		for _, d := range devices {
			if d.ResourceName == "" {
				continue
			}
			rn := corev1.ResourceName(d.ResourceName)
			mapped.Insert(rn)
			switch {
			case d.Allocatable != nil:
				serves[rn] += int64(*d.Allocatable)
			case d.Healthy != nil && *d.Healthy:
				serves[rn]++
			}
		}
		held := map[corev1.ResourceName]int64{}
		for _, d := range w.claims {
			if d.Status.NodeName == name && model.HoldsGPUs(d.Status.Phase) {
				if c := w.classes[d.Spec.ClassName]; c != nil && c.Spec.Source.DevicePlugin != nil {
					held[corev1.ResourceName(c.Spec.Source.DevicePlugin.ResourceName)] += int64(d.Spec.Count)
				}
			}
		}
		for rn, units := range held {
			// kubelet's count caps the agent's, which is as old as the agent's
			// last write.
			q := n.Status.Allocatable[rn]
			if !mapped.Has(rn) || q.Value() < serves[rn] {
				serves[rn] = q.Value()
			}
			if units <= serves[rn] {
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
				w.lost[name].healthy[rn] = serves[rn]
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
	for _, d := range w.claims {
		s.Claims = append(s.Claims, model.ClaimFrom(d))
	}
	for _, n := range w.nodes {
		mn := model.NodeFrom(n)
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
	// A Lost claim covers its VMI's pod for budgets, which already count
	// the claim, but not for node capacity: it holds no units, so the pod
	// still running on its node holds them itself.
	coveredForBudget := map[types.NamespacedName]map[corev1.ResourceName]int64{}
	coveredForCapacity := map[types.NamespacedName]map[corev1.ResourceName]int64{}
	add := func(m map[types.NamespacedName]map[corev1.ResourceName]int64, k types.NamespacedName, rn corev1.ResourceName, n int64) {
		if m[k] == nil {
			m[k] = map[corev1.ResourceName]int64{}
		}
		m[k][rn] += n
	}
	for _, d := range w.claims {
		rn := w.resourceName(d.Spec.ClassName)
		if d.Status.AttachedTo == nil || rn == "" {
			continue
		}
		k := key(d.Namespace, d.Status.AttachedTo.VMI)
		add(coveredForBudget, k, rn, int64(d.Spec.Count))
		if holds(d) {
			add(coveredForCapacity, k, rn, int64(d.Spec.Count))
		}
	}
	for i := range pods {
		p := &pods[i]
		// Only a pod its VMI lists as active is covered: whoever creates a
		// pod sets its owner references.
		vmi, _ := w.launcherVMI(p)
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
			// budget already counts them through the Lost claim.
			s.Pods = append(s.Pods, model.Pod{NodeName: p.Spec.NodeName, Units: extra})
		}
	}
	return s
}

// setClaim mirrors a placement change, or a claim this pass created, into
// the snapshot, so later decisions in the same pass see it.
func (w *world) setClaim(d *gpuv1alpha1.GPUClaim) {
	for i := range w.snap.Claims {
		if w.snap.Claims[i].Namespace == d.Namespace && w.snap.Claims[i].Name == d.Name {
			w.snap.Claims[i] = model.ClaimFrom(d)
			return
		}
	}
	w.snap.Claims = append(w.snap.Claims, model.ClaimFrom(d))
}

func (w *world) resourceName(class string) corev1.ResourceName {
	if c := w.classes[class]; c != nil && c.Spec.Source.DevicePlugin != nil {
		return corev1.ResourceName(c.Spec.Source.DevicePlugin.ResourceName)
	}
	return ""
}

func (w *world) claim(namespace, name string) *gpuv1alpha1.GPUClaim {
	for _, d := range w.claims {
		if d.Namespace == namespace && d.Name == name {
			return d
		}
	}
	return nil
}

func isTemplate(d *gpuv1alpha1.GPUClaim) bool {
	_, ok := d.Labels[gpu.LabelTemplateClaim]
	return ok
}
