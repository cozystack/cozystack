/*
Copyright 2025 The Cozystack Authors.

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

package cozyvaluesreplicator

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SecretReplicatorReconciler replicates a source secret to namespaces matching a label selector.
type SecretReplicatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Source of truth:
	SourceNamespace string
	SecretName      string

	// Namespaces to replicate into:
	// (e.g. labels.SelectorFromSet(labels.Set{"tenant":"true"}), or metav1.LabelSelectorAsSelector(...))
	TargetNamespaceSelector labels.Selector
}

func (r *SecretReplicatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// 1) Primary watch for requirement (b):
	//    Reconcile any Secret named r.SecretName in any namespace (includes source too).
	//    This keeps Secrets in cache and causes "copy changed -> reconcile it" to happen.
	secretNameOnly := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == r.SecretName
	})

	// 2) Secondary watch for requirement (c):
	//    When the *source* Secret changes, fan-out reconcile requests to every matching namespace.
	onlySourceSecret := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isSourceSecret(e.Object, r) },
		UpdateFunc: func(e event.UpdateEvent) bool { return isSourceSecret(e.ObjectNew, r) },
		DeleteFunc: func(e event.DeleteEvent) bool { return isSourceSecret(e.Object, r) },
		GenericFunc: func(e event.GenericEvent) bool {
			return isSourceSecret(e.Object, r)
		},
	}

	// Fan-out mapper for source Secret events -> one request per matching target namespace.
	fanOutOnSourceSecret := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		// List namespaces *from the cache* (because we also watch Namespaces below).
		var nsList corev1.NamespaceList
		if err := r.List(ctx, &nsList); err != nil {
			// If list fails, best-effort: return nothing; reconcile will be retried by next event.
			return nil
		}

		reqs := make([]reconcile.Request, 0, len(nsList.Items))
		for i := range nsList.Items {
			ns := &nsList.Items[i]
			if ns.Name == r.SourceNamespace {
				continue
			}
			if r.TargetNamespaceSelector != nil && !r.TargetNamespaceSelector.Matches(labels.Set(ns.Labels)) {
				continue
			}
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: ns.Name,
					Name:      r.SecretName,
				},
			})
		}
		return reqs
	})

	// 3) Namespace watch for requirement (a):
	//    When a namespace is created/updated to match selector, enqueue reconcile for the Secret copy in that namespace.
	enqueueOnNamespaceMatch := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		ns, ok := obj.(*corev1.Namespace)
		if !ok {
			return nil
		}
		if ns.Name == r.SourceNamespace {
			return nil
		}
		if r.TargetNamespaceSelector != nil && !r.TargetNamespaceSelector.Matches(labels.Set(ns.Labels)) {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: ns.Name,
				Name:      r.SecretName,
			},
		}}
	})

	// Only trigger from namespace events where the label match may be (or become) true.
	// (You can keep this simple; it's fine if it fires on any update—your Reconcile should be idempotent.)
	namespaceMayMatter := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			return ok && (r.TargetNamespaceSelector == nil || r.TargetNamespaceSelector.Matches(labels.Set(ns.Labels)))
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNS, okOld := e.ObjectOld.(*corev1.Namespace)
			newNS, okNew := e.ObjectNew.(*corev1.Namespace)
			if !okOld || !okNew {
				return false
			}
			// Fire if it matches now OR matched before (covers transitions both ways; reconcile can decide what to do).
			oldMatch := r.TargetNamespaceSelector == nil || r.TargetNamespaceSelector.Matches(labels.Set(oldNS.Labels))
			newMatch := r.TargetNamespaceSelector == nil || r.TargetNamespaceSelector.Matches(labels.Set(newNS.Labels))
			return oldMatch || newMatch
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false }, // nothing to do on namespace delete
		GenericFunc: func(event.GenericEvent) bool { return false },
	}

	return ctrl.NewControllerManagedBy(mgr).
		// (b) Watch all Secrets with the chosen name; this also ensures Secret objects are cached.
		For(&corev1.Secret{}, builder.WithPredicates(secretNameOnly)).

		// (c) Add a second watch on Secret, but only for the source secret, and fan-out to all namespaces.
		Watches(
			&corev1.Secret{},
			fanOutOnSourceSecret,
			builder.WithPredicates(onlySourceSecret),
		).

		// (a) Watch Namespaces so they're cached and so "namespace appears / starts matching" enqueues reconcile.
		Watches(
			&corev1.Namespace{},
			enqueueOnNamespaceMatch,
			builder.WithPredicates(namespaceMayMatter),
		).
		Complete(r)
}

func isSourceSecret(obj client.Object, r *SecretReplicatorReconciler) bool {
	if obj == nil {
		return false
	}
	return obj.GetNamespace() == r.SourceNamespace && obj.GetName() == r.SecretName
}

func (r *SecretReplicatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Ignore requests that don't match our secret name or are for the source namespace
	if req.Name != r.SecretName || req.Namespace == r.SourceNamespace {
		return ctrl.Result{}, nil
	}

	// Verify the target namespace still exists and matches the selector
	targetNamespace := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: req.Namespace}, targetNamespace); err != nil {
		if apierrors.IsNotFound(err) {
			// Namespace doesn't exist, nothing to do
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get target namespace", "namespace", req.Namespace)
		return ctrl.Result{}, err
	}

	// Check if namespace still matches the selector
	if r.TargetNamespaceSelector != nil && !r.TargetNamespaceSelector.Matches(labels.Set(targetNamespace.Labels)) {
		// Namespace no longer matches selector, delete the replicated secret if it exists
		replicatedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: req.Namespace,
				Name:      req.Name,
			},
		}
		if err := r.Delete(ctx, replicatedSecret); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to delete replicated secret from non-matching namespace",
				"namespace", req.Namespace, "secret", req.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Get the source secret
	originalSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.SourceNamespace, Name: r.SecretName}, originalSecret); err != nil {
		if apierrors.IsNotFound(err) {
			// Source secret doesn't exist, delete the replicated secret if it exists
			replicatedSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: req.Namespace,
					Name:      req.Name,
				},
			}
			if err := r.Delete(ctx, replicatedSecret); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete replicated secret after source secret deletion",
					"namespace", req.Namespace, "secret", req.Name)
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get source secret",
			"namespace", r.SourceNamespace, "secret", r.SecretName)
		return ctrl.Result{}, err
	}

	// Create or update the replicated secret
	replicatedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: req.Namespace,
			Name:      req.Name,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, replicatedSecret, func() error {
		// Copy the secret data and type from the source
		replicatedSecret.Data = make(map[string][]byte)
		for k, v := range originalSecret.Data {
			replicatedSecret.Data[k] = v
		}
		replicatedSecret.Type = originalSecret.Type

		// Copy labels and annotations from source (if any)
		if originalSecret.Labels != nil {
			if replicatedSecret.Labels == nil {
				replicatedSecret.Labels = make(map[string]string)
			}
			for k, v := range originalSecret.Labels {
				replicatedSecret.Labels[k] = v
			}
		}
		if originalSecret.Annotations != nil {
			if replicatedSecret.Annotations == nil {
				replicatedSecret.Annotations = make(map[string]string)
			}
			for k, v := range originalSecret.Annotations {
				replicatedSecret.Annotations[k] = v
			}
		}

		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to create or update replicated secret",
			"namespace", req.Namespace, "secret", req.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}
