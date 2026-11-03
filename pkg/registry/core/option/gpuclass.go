// SPDX-License-Identifier: Apache-2.0

package option

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

var (
	gvrGPUClasses = cozyv1alpha1.GroupVersion.WithResource("gpuclasses")
	gvrNamespaces = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
)

// GPUBudgetFunc returns how many more units of a class the pool of a
// namespace may declare, and false when no GPU budget limits it.
type GPUBudgetFunc func(ctx context.Context, namespace, class string) (int64, bool, error)

// GPUClassProvider is the GPU catalog: every usable class, with
// what bounds an entry of it. Every authenticated user reads the catalog,
// so an item carries class-wide figures only and nothing about a node.
// budget may be nil, which leaves remainingBudget out.
func GPUClassProvider(dyn dynamic.Interface, budget GPUBudgetFunc) providerFunc {
	return func(ctx context.Context, namespace string) ([]corev1alpha1.OptionItem, error) {
		list, err := dyn.Resource(gvrGPUClasses).List(ctx, listOpts())
		if err != nil {
			return nil, err
		}
		entitled, err := namespaceEntitled(ctx, dyn, namespace)
		if err != nil {
			return nil, err
		}
		var items []corev1alpha1.OptionItem
		for i := range list.Items {
			c := &cozyv1alpha1.GPUClass{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, c); err != nil {
				return nil, err
			}
			if c.DeletionTimestamp != nil ||
				meta.IsStatusConditionTrue(c.Status.Conditions, cozyv1alpha1.GPUClassResourceNameConflict) ||
				meta.IsStatusConditionTrue(c.Status.Conditions, cozyv1alpha1.GPUClassResourceNameNotPermitted) {
				continue
			}
			item := corev1alpha1.OptionItem{
				Value:          c.Name,
				Label:          c.Spec.DisplayName,
				Description:    classDescription(&c.Spec),
				Free:           widen(c.Status.Free),
				MaxFreePerNode: widen(c.Status.MaxFreePerNode),
			}
			if n, ok := model.EffectiveMaxCount(&c.Spec); ok {
				item.MaxCount = widen(&n)
			}
			switch {
			case c.Spec.Mode == cozyv1alpha1.GPUModeVGPU && !entitled:
				item.RemainingBudget = new(int64)
			case budget != nil:
				left, limited, err := budget(ctx, namespace, c.Name)
				if err != nil {
					return nil, err
				}
				if limited {
					item.RemainingBudget = &left
				}
			}
			items = append(items, item)
		}
		sortItems(items)
		return items, nil
	}
}

func namespaceEntitled(ctx context.Context, dyn dynamic.Interface, namespace string) (bool, error) {
	if namespace == "" {
		return false, nil
	}
	ns, err := dyn.Resource(gvrNamespaces).Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return model.Entitled(ns.GetLabels()), nil
}

func classDescription(spec *cozyv1alpha1.GPUClassSpec) string {
	parts := []string{strings.TrimSpace(spec.Vendor + " " + spec.Model), string(spec.Mode)}
	if spec.Profile != "" {
		parts = append(parts, spec.Profile)
	}
	if spec.Memory != nil {
		parts = append(parts, spec.Memory.String())
	}
	return strings.Join(parts, ", ")
}

func widen(v *int32) *int64 {
	if v == nil {
		return nil
	}
	w := int64(*v)
	return &w
}
