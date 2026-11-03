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
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

const (
	reasonSharedResourceName   = "SharedResourceName"
	reasonUniqueResourceName   = "UniqueResourceName"
	reasonNotInPermittedDevice = "NotInPermittedHostDevices"
	reasonPermitted            = "Permitted"

	// permittedRecheck bounds how long a change to the KubeVirt
	// permittedHostDevices table takes to reach ResourceNameNotPermitted:
	// the table is read on every pass and not watched.
	permittedRecheck = time.Minute
)

var kubeVirtGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "KubeVirt"}

// StatusReconciler writes ownedResourceName, free, maxFreePerNode,
// ResourceNameConflict and ResourceNameNotPermitted on every GPUClass in one
// pass, since name ownership depends on the other classes. It is the only
// writer of ownedResourceName; leader election keeps it single in steady
// state but does not fence an overlapping old leader, whose duplicate
// acknowledgements the next pass keeps as a conflict. The status is readable
// by every authenticated user, so it names no node.
type StatusReconciler struct {
	Client client.Client
	// Reader is uncached: the classes and the KubeVirt CR are read on each
	// pass, so ownership is not decided from a lagging cache and the
	// controller holds no KubeVirt watch.
	Reader client.Reader
}

func (r *StatusReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	permitted, err := r.permittedResourceNames(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Name ownership is decided from an uncached read; each update is
	// checked against the resourceVersion of that read.
	var classList cozyv1alpha1.GPUClassList
	if err := r.Reader.List(ctx, &classList); err != nil {
		return ctrl.Result{}, err
	}
	var nodeList corev1.NodeList
	if err := r.Client.List(ctx, &nodeList); err != nil {
		return ctrl.Result{}, err
	}
	var podList corev1.PodList
	if err := r.Client.List(ctx, &podList); err != nil {
		return ctrl.Result{}, err
	}

	classes := make([]model.Class, len(classList.Items))
	names := sets.New[corev1.ResourceName]()
	for i := range classList.Items {
		classes[i] = model.ClassFrom(&classList.Items[i])
		names.Insert(classes[i].ResourceName)
	}
	nodes := make([]model.Node, len(nodeList.Items))
	for i := range nodeList.Items {
		nodes[i] = model.NodeFrom(&nodeList.Items[i])
	}
	var pods []model.Pod
	for i := range podList.Items {
		if p, ok := model.PodFrom(&podList.Items[i], names); ok {
			pods = append(pods, p)
		}
	}
	// A candidate acquires its requested name in the same status update
	// that releases the name it held before.
	candidates := model.ResourceNameCandidates(classes)
	for i := range classes {
		if rn := classes[i].ResourceName; rn != "" && candidates[rn] == classes[i].UID {
			classes[i].OwnedResourceName = rn
		}
	}
	conflicts := model.Conflicts(classes)
	holder := map[types.UID]string{}
	for _, c := range classes {
		holder[c.UID] = c.Name
	}

	classCapacity.Reset()
	classUsed.Reset()
	var errs []error
	for i := range classList.Items {
		obj, c := &classList.Items[i], classes[i]
		var free, maxFree, capacity int64
		for _, n := range nodes {
			f := model.FreeOnNode(c, n, pods)
			free += f
			maxFree = max(maxFree, f)
			capacity += n.Allocatable[c.ResourceName]
		}
		var used int64
		for _, p := range pods {
			if p.NodeName != "" {
				used += p.Units[c.ResourceName]
			}
		}
		classCapacity.WithLabelValues(c.Name).Set(float64(capacity))
		classUsed.WithLabelValues(c.Name).Set(float64(used))

		before := obj.Status.DeepCopy()
		obj.Status.Free = ptr.To(int32(free))
		obj.Status.MaxFreePerNode = ptr.To(int32(maxFree))
		obj.Status.OwnedResourceName = string(c.OwnedResourceName)
		conflict := metav1.Condition{Type: cozyv1alpha1.GPUClassResourceNameConflict, Status: metav1.ConditionFalse,
			Reason: reasonUniqueResourceName, ObservedGeneration: obj.Generation}
		if conflicts.Has(c.Name) {
			conflict.Status, conflict.Reason = metav1.ConditionTrue, reasonSharedResourceName
			if owner, ok := candidates[c.ResourceName]; ok {
				conflict.Message = fmt.Sprintf("resource name %s belongs to GPUClass %s; this class is not offered", c.ResourceName, holder[owner])
			} else {
				conflict.Message = fmt.Sprintf("more than one GPUClass holds resource name %s; none of them is offered until one releases it", c.ResourceName)
			}
		}
		notPermitted := metav1.Condition{Type: cozyv1alpha1.GPUClassResourceNameNotPermitted, Status: metav1.ConditionFalse,
			Reason: reasonPermitted, ObservedGeneration: obj.Generation}
		if permitted != nil && !permitted.Has(string(c.ResourceName)) {
			notPermitted.Status, notPermitted.Reason = metav1.ConditionTrue, reasonNotInPermittedDevice
			notPermitted.Message = fmt.Sprintf("resource name %s is not in the KubeVirt permittedHostDevices table, so no VM of this class can start", c.ResourceName)
		}
		meta.SetStatusCondition(&obj.Status.Conditions, conflict)
		meta.SetStatusCondition(&obj.Status.Conditions, notPermitted)
		if equality.Semantic.DeepEqual(before, &obj.Status) {
			continue
		}
		errs = append(errs, client.IgnoreNotFound(r.Client.Status().Update(ctx, obj)))
	}
	return ctrl.Result{RequeueAfter: permittedRecheck}, errors.Join(errs...)
}

// permittedResourceNames returns nil, which KubeVirt reads as no table,
// where KubeVirt or its CR is absent.
func (r *StatusReconciler) permittedResourceNames(ctx context.Context) (sets.Set[string], error) {
	kv := &unstructured.Unstructured{}
	kv.SetGroupVersionKind(kubeVirtGVK)
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: model.KubeVirtNamespace, Name: model.KubeVirtName}, kv)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the KubeVirt CR: %w", err)
	}
	return model.PermittedResourceNames(kv), nil
}

// nodeCapacityChanged drops node updates that cannot change free units,
// such as the kubelet's heartbeats.
var nodeCapacityChanged = predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
	o, ok1 := e.ObjectOld.(*corev1.Node)
	n, ok2 := e.ObjectNew.(*corev1.Node)
	if !ok1 || !ok2 {
		return true
	}
	return o.Spec.Unschedulable != n.Spec.Unschedulable ||
		!equality.Semantic.DeepEqual(o.Spec.Taints, n.Spec.Taints) ||
		!equality.Semantic.DeepEqual(o.Status.Allocatable, n.Status.Allocatable) ||
		model.Ready(o) != model.Ready(n)
}}

// requestsExtendedResource keeps the events of pods that can change GPU
// usage without resolving the class resource names on every pod event.
func requestsExtendedResource(o client.Object) bool {
	p, ok := o.(*corev1.Pod)
	if !ok {
		return false
	}
	for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
		for i := range cs {
			for _, rl := range []corev1.ResourceList{cs[i].Resources.Limits, cs[i].Resources.Requests} {
				for name := range rl {
					if strings.Contains(string(name), "/") {
						return true
					}
				}
			}
		}
	}
	return false
}
