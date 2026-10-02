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

// Package backendca copies a backend's CA bundle into the ConfigMap a
// BackendTLSPolicy validates the backend against.
//
// Gateway API reads that CA from the ca.crt key of a ConfigMap in the
// policy's namespace, and that is the only reference kind it requires
// every implementation to support. Operators publish their serving CA
// under keys of their own choosing (KubeVirt under ca-bundle, CDI under
// ca-bundle.crt) and rotate it, so a one-off copy would go stale.
package backendca

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// SourceAnnotation on a BackendTLSPolicy names "<configmap>/<key>" in
// the policy's namespace to copy into the ca.crt key of the ConfigMap
// the policy's first ConfigMap caCertificateRef names.
const SourceAnnotation = "gateway.cozystack.io/ca-bundle-from"

// resync bounds how long a rotated CA takes to reach the policy. The
// sources are read uncached rather than watched, because watching them
// would cache every ConfigMap in the cluster for two of them.
const resync = 5 * time.Minute

// Reconciler keeps the CA copy of each annotated BackendTLSPolicy.
type Reconciler struct {
	client.Client
	// Reader reads the source and target ConfigMaps past the cache.
	Reader client.Reader
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=backendtlspolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;create;update

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	policy := &gatewayv1.BackendTLSPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	from := policy.Annotations[SourceAnnotation]
	if from == "" {
		return ctrl.Result{}, nil
	}
	srcName, srcKey, ok := strings.Cut(from, "/")
	if !ok || srcName == "" || srcKey == "" {
		logger.Info("ignoring malformed annotation, want <configmap>/<key>", "annotation", SourceAnnotation, "value", from)
		return ctrl.Result{}, nil
	}
	targetName := ""
	for _, ref := range policy.Spec.Validation.CACertificateRefs {
		if ref.Group == "" && ref.Kind == "ConfigMap" {
			targetName = string(ref.Name)
			break
		}
	}
	if targetName == "" {
		logger.Info("policy names no ConfigMap CA to write", "policy", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	src := &corev1.ConfigMap{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: srcName}, src); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: resync}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get source ConfigMap %s: %w", srcName, err)
	}
	bundle := src.Data[srcKey]
	if bundle == "" {
		return ctrl.Result{RequeueAfter: resync}, nil
	}
	desired := map[string]string{"ca.crt": bundle}

	target := &corev1.ConfigMap{}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: targetName}, target)
	switch {
	case apierrors.IsNotFound(err):
		target = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: targetName, Namespace: policy.Namespace},
			Data:       desired,
		}
		if err := controllerutil.SetControllerReference(policy, target, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, target); err != nil {
			return ctrl.Result{}, fmt.Errorf("create ConfigMap %s: %w", targetName, err)
		}
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get ConfigMap %s: %w", targetName, err)
	default:
		if !metav1.IsControlledBy(target, policy) {
			return ctrl.Result{}, fmt.Errorf("ConfigMap %s/%s exists but is not owned by BackendTLSPolicy %s; refusing to overwrite it", policy.Namespace, targetName, policy.Name)
		}
		if !maps.Equal(target.Data, desired) {
			target.Data = desired
			if err := r.Update(ctx, target); err != nil {
				return ctrl.Result{}, fmt.Errorf("update ConfigMap %s: %w", targetName, err)
			}
		}
	}
	return ctrl.Result{RequeueAfter: resync}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("backendca").
		For(&gatewayv1.BackendTLSPolicy{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetAnnotations()[SourceAnnotation] != ""
		}))).
		Complete(r)
}
