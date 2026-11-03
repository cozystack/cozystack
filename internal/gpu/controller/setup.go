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

// Package controller computes GPUClass status and delivers vGPU license
// copies in cozystack-controller.
package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// Setup adds the GPU reconcilers to mgr. secretMeta is a cache of Secret
// metadata across the cluster: the manager's own Secret cache is scoped
// by label to other reconcilers' Secrets, and a metadata watch on it would
// inherit that scope and never see a license Secret.
//
// The license copier runs even when enabled is false, to delete the
// copies; the status controller runs only when it is true, since it
// caches every Node.
func Setup(mgr manager.Manager, enabled bool, secretMeta cache.Cache) error {
	lic := &LicenseReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Secrets: secretMeta, Enabled: enabled}
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err := ctrl.NewControllerManagedBy(mgr).
		Named("gpuclass-license").
		For(&cozyv1alpha1.GPUClass{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(lic.allClasses),
			builder.WithPredicates(predicate.LabelChangedPredicate{})).
		WatchesRawSource(crsource.Kind(secretMeta, secret, handler.TypedEnqueueRequestsFromMapFunc(lic.secretToClasses))).
		Complete(lic)
	if err != nil || !enabled {
		return err
	}

	pass := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("gpuclass-status").
		Watches(&cozyv1alpha1.GPUClass{}, pass, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Node{}, pass, builder.WithPredicates(nodeCapacityChanged)).
		Watches(&corev1.Pod{}, pass, builder.WithPredicates(predicate.NewPredicateFuncs(requestsExtendedResource))).
		Complete(&StatusReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()})
}
