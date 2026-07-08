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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

// +kubebuilder:rbac:groups=network.cozystack.io,resources=exposureclasses/finalizers,verbs=update

// ClassReconciler owns the lifecycle cleanup of an ExposureClass. The
// class-level pool/announcer CRs carry no OwnerReference (they are
// cross-scope), so when an ExposureClass is deleted nothing else reclaims
// them — this reconciler does, via a finalizer. It does not render the
// pool; that stays driven by ServiceExposure reconciles, which know the
// namespace set.
//
// Eventual-consistency note: a ServiceExposure reconcile running against a
// stale (pre-deletionTimestamp) cached class could recreate the pool this
// controller just GC'd. reconcileClass guards against this by skipping a
// terminating class; once the watch cache catches up, both controllers
// agree. An OwnerReference would make this atomic, but is impossible here
// (the pool is cluster-scoped / cross-namespace relative to the class).
type ClassReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	class := &networkv1alpha1.ExposureClass{}
	if err := r.Get(ctx, req.NamespacedName, class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !class.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(class, exposureFinalizer) {
			if err := reclaimClassResources(ctx, r.Client, class.Name, map[objectKey]struct{}{}); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(class, exposureFinalizer)
			if err := r.Update(ctx, class); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(class, exposureFinalizer) {
		if err := r.Update(ctx, class); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	return ctrl.Result{}, nil
}

func (r *ClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("exposureclass-controller").
		For(&networkv1alpha1.ExposureClass{}).
		Complete(r)
}
