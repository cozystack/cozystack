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

// Package controller runs the GPU binder, the class and claim lifecycle,
// usage records and metrics in cozystack-controller.
package controller

import (
	"context"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

// Setup adds the GPU reconciler to mgr. Finalizers and usage records are
// always reconciled, so a disabled feature never hangs a deletion; with
// cfg.Enabled it also places claims and watches KubeVirt objects, which
// exist only where the feature is enabled.
func Setup(mgr manager.Manager, cfg gpu.Config) error {
	r := &Reconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Cache: mgr.GetCache(), Config: cfg,
		Recorder: mgr.GetEventRecorder("gpu-binder")}
	all := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: passKey}}
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("gpu-binder").
		Watches(&gpuv1alpha1.GPUClass{}, all).
		Watches(&gpuv1alpha1.GPUClaim{}, all).
		Watches(&cozyv1alpha1.Workload{}, all, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool { return model.OwnedByClaim(o) })))
	if cfg.Enabled {
		b = b.
			Watches(&gpuv1alpha1.GPUNode{}, all).
			Watches(&corev1.Node{}, all, builder.WithPredicates(nodePlacementChanged)).
			Watches(&corev1.Namespace{}, all, builder.WithPredicates(predicate.LabelChangedPredicate{})).
			Watches(&helmv2.HelmRelease{}, all, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
				return o.GetLabels()[appsv1alpha1.ApplicationKindLabel] == tenantKind
			}))).
			Watches(&corev1.Pod{}, all, builder.WithPredicates(predicate.NewPredicateFuncs(podMatters)))
	}
	c, err := b.Build(r)
	if err != nil || !cfg.Enabled {
		return err
	}
	srcs := make([]source.TypedSource[reconcile.Request], 0, 3)
	for _, gvk := range []schema.GroupVersionKind{vmiGVK, vmGVK, kubeVirtGVK} {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		srcs = append(srcs, source.Kind[client.Object](mgr.GetCache(), obj, all))
	}
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		return watchWhenServed(ctx, mgr.GetRESTMapper(), kubeVirtPoll, c, srcs)
	}))
}

// kubeVirtPoll is how often the binder looks again for the KubeVirt kinds.
const kubeVirtPoll = 5 * time.Second

// watchWhenServed adds the KubeVirt watches to the binder once KubeVirt
// serves their kinds: a watch on a kind no CRD serves fails the
// controller's start, and on a fresh install GPUs are enabled before
// KubeVirt's operator installs its CRDs. Until then a pass reads no
// KubeVirt object (listKubeVirt).
func watchWhenServed(ctx context.Context, mapper meta.RESTMapper, interval time.Duration,
	c interface {
		Watch(source.TypedSource[reconcile.Request]) error
	}, srcs []source.TypedSource[reconcile.Request]) error {
	err := WaitForKinds(ctx, mapper, interval, func(k schema.GroupVersionKind, err error) {
		log.FromContext(ctx).Info("waiting for a KubeVirt kind the GPU binder watches", "kind", k.String(), "reason", err.Error())
	}, vmiGVK, vmGVK, kubeVirtGVK)
	if err != nil {
		return nil
	}
	for _, src := range srcs {
		if err := c.Watch(src); err != nil {
			return err
		}
	}
	return nil
}

// nodePlacementChanged drops node updates that cannot change placement,
// such as the kubelet's periodic status writes.
var nodePlacementChanged = predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
	o, ok1 := e.ObjectOld.(*corev1.Node)
	n, ok2 := e.ObjectNew.(*corev1.Node)
	if !ok1 || !ok2 {
		return true
	}
	return o.Spec.Unschedulable != n.Spec.Unschedulable ||
		!equality.Semantic.DeepEqual(o.Spec.Taints, n.Spec.Taints) ||
		!equality.Semantic.DeepEqual(o.Labels, n.Labels) ||
		!equality.Semantic.DeepEqual(o.Status.Allocatable, n.Status.Allocatable) ||
		model.Ready(o) != model.Ready(n)
}}

// podMatters keeps the events of launcher pods with claim references, gated
// or pinned, and of pods that can change GPU usage.
func podMatters(o client.Object) bool {
	return o.GetLabels()[gpu.LabelClaimReference] == gpu.LabelValueTrue || requestsExtendedResource(o)
}

// requestsExtendedResource keeps pod events that can change GPU usage
// without parsing the effective resource-name set on every pod event.
func requestsExtendedResource(o client.Object) bool {
	p, ok := o.(*corev1.Pod)
	if !ok {
		return false
	}
	for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
		for i := range cs {
			for _, rl := range []corev1.ResourceList{cs[i].Resources.Limits, cs[i].Resources.Requests} {
				for name := range rl {
					// Every virt-launcher pod requests devices.kubevirt.io/kvm.
					if strings.Contains(string(name), "/") && !strings.HasPrefix(string(name), "devices.kubevirt.io/") {
						return true
					}
				}
			}
		}
	}
	return false
}
