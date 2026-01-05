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

package operator

import (
	"context"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// LabelPackage marks HelmReleases created by Package reconciler.
	LabelPackage = "cozystack.io/package"
	// LabelReplicatedFrom marks secrets replicated by this controller.
	LabelReplicatedFrom = "cozystack.io/replicated-from"
	// SourceNamespace is the namespace where the source secret is located.
	SourceNamespace = "cozy-system"
)

// SecretReplicatorReconciler replicates cozystack-values secret to namespaces
// where Package reconciler installs HelmReleases.
type SecretReplicatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch

// Reconcile replicates the cozystack-values secret to namespaces with our HelmReleases.
func (r *SecretReplicatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the source secret from cozy-system namespace
	sourceSecret := &corev1.Secret{}
	sourceKey := types.NamespacedName{
		Name:      SecretCozystackValues,
		Namespace: SourceNamespace,
	}

	if err := r.Get(ctx, sourceKey, sourceSecret); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("source secret not found, skipping replication", "secret", SecretCozystackValues, "namespace", SourceNamespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// List all HelmReleases with our label
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, client.HasLabels{LabelPackage}); err != nil {
		return ctrl.Result{}, err
	}

	// Collect unique namespaces where we have HelmReleases
	targetNamespaces := make(map[string]bool)
	for _, hr := range hrList.Items {
		// Skip source namespace
		if hr.Namespace == SourceNamespace {
			continue
		}
		targetNamespaces[hr.Namespace] = true
	}

	// Replicate secret to each target namespace
	for ns := range targetNamespaces {
		if err := r.replicateSecret(ctx, sourceSecret, ns); err != nil {
			logger.Error(err, "failed to replicate secret", "namespace", ns)
			// Continue with other namespaces
			continue
		}
		logger.V(1).Info("replicated secret", "namespace", ns)
	}

	// Cleanup secrets from namespaces where we no longer have HelmReleases
	if err := r.cleanupOrphanedSecrets(ctx, targetNamespaces); err != nil {
		logger.Error(err, "failed to cleanup orphaned secrets")
		// Don't return error, cleanup is best-effort
	}

	return ctrl.Result{}, nil
}

// replicateSecret copies the source secret to the target namespace.
func (r *SecretReplicatorReconciler) replicateSecret(ctx context.Context, source *corev1.Secret, targetNamespace string) error {
	target := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      source.Name,
			Namespace: targetNamespace,
			Labels: map[string]string{
				LabelReplicatedFrom: SourceNamespace,
			},
		},
		Type: source.Type,
		Data: source.Data,
	}

	// Check if secret already exists
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, target)
	} else if err != nil {
		return err
	}

	// Update existing secret
	existing.Data = source.Data
	existing.Type = source.Type
	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	existing.Labels[LabelReplicatedFrom] = SourceNamespace

	return r.Update(ctx, existing)
}

// cleanupOrphanedSecrets removes replicated secrets from namespaces
// where we no longer have HelmReleases.
func (r *SecretReplicatorReconciler) cleanupOrphanedSecrets(ctx context.Context, activeNamespaces map[string]bool) error {
	logger := log.FromContext(ctx)

	// List all secrets with our replicated label
	secretList := &corev1.SecretList{}
	if err := r.List(ctx, secretList, client.MatchingLabels{
		LabelReplicatedFrom: SourceNamespace,
	}); err != nil {
		return err
	}

	// Delete secrets that are not in active namespaces
	for _, secret := range secretList.Items {
		if secret.Name != SecretCozystackValues {
			continue
		}
		if !activeNamespaces[secret.Namespace] {
			logger.Info("deleting orphaned secret", "namespace", secret.Namespace)
			if err := r.Delete(ctx, &secret); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to delete orphaned secret", "namespace", secret.Namespace)
			}
		}
	}

	return nil
}

// sourceSecretPredicate filters events to only the source secret in cozy-system.
func sourceSecretPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == SecretCozystackValues && obj.GetNamespace() == SourceNamespace
	})
}

// packageHelmReleasePredicate filters HelmReleases to only those with our label.
func packageHelmReleasePredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		labels := obj.GetLabels()
		if labels == nil {
			return false
		}
		_, hasLabel := labels[LabelPackage]
		return hasLabel
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *SecretReplicatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-secret-replicator").
		// Watch only the source secret in cozy-system namespace
		For(&corev1.Secret{}, builder.WithPredicates(sourceSecretPredicate())).
		// Watch HelmReleases with our label to trigger replication
		Watches(
			&helmv2.HelmRelease{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				// Always enqueue the source secret to trigger full reconciliation
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name:      SecretCozystackValues,
						Namespace: SourceNamespace,
					},
				}}
			}),
			builder.WithPredicates(packageHelmReleasePredicate()),
		).
		Complete(r)
}
