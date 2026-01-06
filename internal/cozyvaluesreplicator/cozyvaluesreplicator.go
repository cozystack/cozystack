package cozyvaluesreplicator

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Reconciler fields this setup relies on.
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
	//    This keeps Secrets in cache and causes “copy changed -> reconcile it” to happen.
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
	// (You can keep this simple; it’s fine if it fires on any update—your Reconcile should be idempotent.)
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

		// (a) Watch Namespaces so they’re cached and so “namespace appears / starts matching” enqueues reconcile.
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
	if req.Name != r.SecretName || req.Namespace == r.SourceNamespace {
		return ctrl.Result{}, nil
	}
	originalSecret := &corev1.Secret{}
	r.Get(ctx, types.NamespacedName{Namespace: r.SourceNamespace, Name: r.SecretName}, originalSecret)
	replicatedSecret := originalSecret.DeepCopy()
	replicatedSecret.Namespace = req.Namespace
	r.Update(ctx, replicatedSecret)
	return ctrl.Result{}, nil
}
