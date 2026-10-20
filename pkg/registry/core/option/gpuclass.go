// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

var (
	gvrGPUClasses = gpuv1alpha1.GroupVersion.WithResource("gpuclasses")
	gvrGPUDevices = gpuv1alpha1.GroupVersion.WithResource("gpudevices")
	gvrNamespaces = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	gvrPods       = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
)

const (
	rootTenantNamespace = "tenant-root"
	tenantPrefix        = "tenant-"
)

// gpuClassProvider is the GPU catalog: every class that takes orders, with
// what bounds an order of it for the requesting tenant. It names no nodes.
func gpuClassProvider(dyn dynamic.Interface) providerFunc {
	return func(ctx context.Context, namespace string) ([]corev1alpha1.OptionItem, error) {
		var classes []gpuv1alpha1.GPUClass
		if err := listInto(ctx, dyn.Resource(gvrGPUClasses), &classes); err != nil {
			return nil, err
		}
		live := make([]gpuv1alpha1.GPUClass, 0, len(classes))
		for _, c := range classes {
			if c.DeletionTimestamp == nil {
				live = append(live, c)
			}
		}
		if len(live) == 0 {
			return nil, nil
		}
		snap, err := budgetSnapshot(ctx, dyn, namespace, classes)
		if err != nil {
			return nil, err
		}
		entitled, err := namespaceEntitled(ctx, dyn, namespace)
		if err != nil {
			return nil, err
		}

		items := make([]corev1alpha1.OptionItem, 0, len(live))
		for i := range live {
			c := &live[i]
			item := corev1alpha1.OptionItem{
				Value:          c.Name,
				Label:          c.Spec.DisplayName,
				Description:    classDescription(&c.Spec),
				Free:           int64Ptr(c.Status.Free),
				MaxFreePerNode: int64Ptr(c.Status.MaxFreePerNode),
			}
			if n, ok := model.EffectiveMaxCount(&c.Spec); ok {
				item.MaxCount = new(int64(n))
			}
			if left, ok := snap.RemainingBudget(namespace, c.Name, entitled); ok {
				item.RemainingBudget = new(left)
			}
			items = append(items, item)
		}
		sortItems(items)
		return items, nil
	}
}

func classDescription(spec *gpuv1alpha1.GPUClassSpec) string {
	parts := []string{strings.TrimSpace(spec.Vendor + " " + spec.Model), string(spec.Mode)}
	if spec.Profile != "" {
		parts = append(parts, spec.Profile)
	}
	if spec.Memory != nil {
		parts = append(parts, spec.Memory.String())
	}
	return strings.Join(parts, ", ")
}

func int64Ptr(v *int32) *int64 {
	if v == nil {
		return nil
	}
	return new(int64(*v))
}

// budgetSnapshot gathers what the GPU budgets of namespace and its ancestors
// depend on: their declared resourceQuotas, every device (an ancestor's
// subtree can span the whole tenant tree), and, only for the resource names
// some ancestor limits with a raw key, the units pods request beyond what
// their attached devices cover.
func budgetSnapshot(ctx context.Context, dyn dynamic.Interface, namespace string, classes []gpuv1alpha1.GPUClass) (*model.Snapshot, error) {
	snap := &model.Snapshot{Classes: map[string]model.Class{}, Quotas: map[string]corev1.ResourceList{}}
	byName := map[string]*gpuv1alpha1.GPUClass{}
	for i := range classes {
		snap.Classes[classes[i].Name] = model.ClassFrom(&classes[i])
		byName[classes[i].Name] = &classes[i]
	}

	ancestors := model.Ancestors(namespace)
	for _, a := range ancestors {
		q, err := declaredQuotas(ctx, dyn, a)
		if err != nil {
			return nil, err
		}
		if len(q) > 0 {
			snap.Quotas[a] = q
		}
	}

	var devices []gpuv1alpha1.GPUDevice
	if err := listInto(ctx, dyn.Resource(gvrGPUDevices), &devices); err != nil {
		return nil, err
	}
	for i := range devices {
		snap.Devices = append(snap.Devices, model.DeviceFrom(&devices[i]))
	}

	rawNames := sets.New[corev1.ResourceName]()
	top := ""
	for _, a := range ancestors {
		for _, c := range snap.Classes {
			if c.ResourceName == "" {
				continue
			}
			if _, ok := model.RawQuotaLimit(snap.Quotas[a], c.ResourceName); ok {
				rawNames.Insert(c.ResourceName)
				top = a
			}
		}
	}
	if rawNames.Len() == 0 {
		return snap, nil
	}

	pods, err := subtreePods(ctx, dyn, top)
	if err != nil {
		return nil, err
	}
	covered := coveredByVMI(devices, byName)
	for i := range pods {
		p := &pods[i]
		var cov map[corev1.ResourceName]int64
		for _, ref := range p.OwnerReferences {
			if ref.Kind == "VirtualMachineInstance" {
				cov = covered[types.NamespacedName{Namespace: p.Namespace, Name: ref.Name}]
			}
		}
		if mp, ok := model.PodFrom(p, rawNames, cov); ok {
			snap.Pods = append(snap.Pods, mp)
		}
	}
	return snap, nil
}

// subtreePods lists pods namespace by namespace over root's subtree rather
// than cluster-wide, since raw keys sit on a few tenants during migration.
func subtreePods(ctx context.Context, dyn dynamic.Interface, root string) ([]corev1.Pod, error) {
	var namespaces []corev1.Namespace
	if err := listInto(ctx, dyn.Resource(gvrNamespaces), &namespaces); err != nil {
		return nil, err
	}
	var out []corev1.Pod
	for i := range namespaces {
		if !model.InSubtree(namespaces[i].Name, root) {
			continue
		}
		var pods []corev1.Pod
		if err := listInto(ctx, dyn.Resource(gvrPods).Namespace(namespaces[i].Name), &pods); err != nil {
			return nil, err
		}
		out = append(out, pods...)
	}
	return out, nil
}

// coveredByVMI sums, per VMI, the units its attached devices account for
// under each resource name, so its virt-launcher pod does not count them a
// second time against a budget. Every attached device covers, in any phase, as
// in the order admission: a Lost device still counts against budgets, unlike
// against a node's capacity.
func coveredByVMI(devices []gpuv1alpha1.GPUDevice, classes map[string]*gpuv1alpha1.GPUClass) map[types.NamespacedName]map[corev1.ResourceName]int64 {
	out := map[types.NamespacedName]map[corev1.ResourceName]int64{}
	for i := range devices {
		d := &devices[i]
		if d.Status.AttachedTo == nil {
			continue
		}
		c := classes[d.Spec.ClassName]
		if c == nil || c.Spec.Source.DevicePlugin == nil {
			continue
		}
		k := types.NamespacedName{Namespace: d.Namespace, Name: d.Status.AttachedTo.VMI}
		if out[k] == nil {
			out[k] = map[corev1.ResourceName]int64{}
		}
		out[k][corev1.ResourceName(c.Spec.Source.DevicePlugin.ResourceName)] += int64(d.Spec.Count)
	}
	return out
}

// declaredQuotas reads the resourceQuotas of the tenant that owns ns from its
// HelmRelease, all keys as written. A tenant x of namespace P owns P-x, or
// tenant-x directly under root, and its release is tenant-x in P.
func declaredQuotas(ctx context.Context, dyn dynamic.Interface, ns string) (corev1.ResourceList, error) {
	if ns != rootTenantNamespace && !strings.HasPrefix(ns, tenantPrefix) {
		return nil, nil
	}
	name, releaseNS := rootTenantNamespace, rootTenantNamespace
	if ns != rootTenantNamespace {
		segments := strings.Split(ns, "-")
		name = tenantPrefix + segments[len(segments)-1]
		releaseNS = model.ParentNamespace(ns)
	}
	hr, err := dyn.Resource(gvrHelmReleases).Namespace(releaseNS).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	raw, _, _ := unstructured.NestedMap(hr.Object, "spec", "values", "resourceQuotas")
	out := corev1.ResourceList{}
	for k, v := range raw {
		q, err := resource.ParseQuantity(fmt.Sprint(v))
		if err != nil {
			continue
		}
		out[corev1.ResourceName(k)] = q
	}
	return out, nil
}

func namespaceEntitled(ctx context.Context, dyn dynamic.Interface, ns string) (bool, error) {
	if ns == "" {
		return false, nil
	}
	obj, err := dyn.Resource(gvrNamespaces).Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return model.Entitled(obj.GetLabels()), nil
}

func listInto[T any](ctx context.Context, r dynamic.ResourceInterface, out *[]T) error {
	list, err := r.List(ctx, listOpts())
	if err != nil {
		return err
	}
	items := make([]T, len(list.Items))
	for i := range list.Items {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &items[i]); err != nil {
			return fmt.Errorf("decode %s %s: %w", list.Items[i].GetKind(), list.Items[i].GetName(), err)
		}
	}
	*out = items
	return nil
}
