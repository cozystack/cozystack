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

package serviceexposure

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

// serviceToExposure maps a Service change back to the ServiceExposure(s)
// in the same namespace that reference it, so a freshly-assigned
// LoadBalancer IP triggers a status refresh.
func (r *Reconciler) serviceToExposure() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(r.mapServiceToExposures)
}

func (r *Reconciler) mapServiceToExposures(ctx context.Context, obj client.Object) []reconcile.Request {
	svc, ok := obj.(*corev1.Service)
	if !ok {
		return nil
	}
	list := &networkv1alpha1.ServiceExposureList{}
	if err := r.List(ctx, list, client.InNamespace(svc.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "list ServiceExposures for service mapper")
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		exp := &list.Items[i]
		if exp.Spec.ServiceRef.Name != svc.Name {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: exp.Namespace, Name: exp.Name,
		}})
	}
	return out
}

// classToExposures maps an ExposureClass change back to every
// ServiceExposure that resolves to it — those naming it explicitly, plus
// every empty-class exposure when the changed class is the cluster default.
func (r *Reconciler) classToExposures() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(r.mapClassToExposures)
}

func (r *Reconciler) mapClassToExposures(ctx context.Context, obj client.Object) []reconcile.Request {
	class, ok := obj.(*networkv1alpha1.ExposureClass)
	if !ok {
		return nil
	}
	isDefault := class.Annotations[networkv1alpha1.IsDefaultExposureClassAnnotation] == "true"

	list := &networkv1alpha1.ServiceExposureList{}
	if err := r.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "list ServiceExposures for class mapper")
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		exp := &list.Items[i]
		if !exposureReferencesClass(exp, class.Name, isDefault) {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: exp.Namespace, Name: exp.Name,
		}})
	}
	return out
}

// exposureReferencesClass reports whether the exposure resolves to the
// named class — either by naming it explicitly, or by leaving the class
// name empty when that class is the cluster default.
func exposureReferencesClass(exp *networkv1alpha1.ServiceExposure, className string, classIsDefault bool) bool {
	name := exp.Spec.ExposureClassName
	return name == className || (name == "" && classIsDefault)
}
