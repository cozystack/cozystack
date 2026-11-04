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
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/workers/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const (
	Finalizer = "cozystack.io/workerclass-in-use"
	poolKind  = "KubernetesNodes"
)

// The chart resolves these names without reading the object, so deleting one
// breaks no render and they get no finalizer.
var builtIn = map[string]bool{"talos": true, "kairos": true}

// +kubebuilder:rbac:groups=cozystack.io,resources=workerclasses,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch

type Reconciler struct {
	client.Client
	// Reader bypasses informer lag when deciding whether a class can disappear.
	Reader client.Reader
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if builtIn[req.Name] {
		return ctrl.Result{}, nil
	}
	ngc := &cozyv1alpha1.WorkerClass{}
	if err := r.Reader.Get(ctx, req.NamespacedName, ngc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if ngc.DeletionTimestamp.IsZero() {
		if controllerutil.AddFinalizer(ngc, Finalizer) {
			return ctrl.Result{}, r.Update(ctx, ngc)
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(ngc, Finalizer) {
		return ctrl.Result{}, nil
	}
	inUse, err := r.referenced(ctx, ngc.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inUse {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	controllerutil.RemoveFinalizer(ngc, Finalizer)
	if err := r.Update(ctx, ngc); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) referenced(ctx context.Context, name string) (bool, error) {
	pools := &helmv2.HelmReleaseList{}
	if err := r.Reader.List(ctx, pools, client.MatchingLabels{appsv1alpha1.ApplicationKindLabel: poolKind}); err != nil {
		return false, fmt.Errorf("listing KubernetesNodes releases: %w", err)
	}
	for i := range pools.Items {
		class, err := className(&pools.Items[i])
		if err != nil {
			return false, fmt.Errorf("reading pool %s/%s: %w", pools.Items[i].Namespace, pools.Items[i].Name, err)
		}
		if class == name {
			return true, nil
		}
	}
	return false, nil
}

// Direct producers store workerClassName in spec.values, as the application
// API does; omission intentionally keeps that producer's pool legacy.
func className(hr *helmv2.HelmRelease) (string, error) {
	if hr.Spec.Values == nil {
		return "", nil
	}
	var values struct {
		WorkerClassName string `json:"workerClassName"`
	}
	if err := json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
		return "", fmt.Errorf("invalid workerClassName values: %w", err)
	}
	return values.WorkerClassName, nil
}

// EnqueueRequestsFromMapFunc maps both sides of an update, so a pool moving
// off a class wakes the class it left as well as the one it joined.
var poolEvents = handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
	hr, ok := o.(*helmv2.HelmRelease)
	if !ok {
		return nil
	}
	name, err := className(hr)
	if err == nil && name != "" && !builtIn[name] {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
	}
	return nil
})

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Reader == nil {
		r.Reader = mgr.GetAPIReader()
	}
	pools := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[appsv1alpha1.ApplicationKindLabel] == poolKind
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("workerclass-controller").
		For(&cozyv1alpha1.WorkerClass{}).
		Watches(&helmv2.HelmRelease{}, poolEvents, builder.WithPredicates(pools)).
		Complete(r)
}
