package fluxplunger

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	annotationLastProcessedVersion = "flux-plunger.cozystack.io/last-processed-version"
	errorMessageNoDeployedReleases = "has no deployed releases"
	fieldManager                   = "flux-client-side-apply"
)

// FluxPlunger watches HelmRelease resources and fixes "has no deployed releases" errors
type FluxPlunger struct {
	client.Client
}

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;delete

// Reconcile handles HelmRelease resources with "has no deployed releases" error
func (r *FluxPlunger) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the HelmRelease
	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, req.NamespacedName, hr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if HelmRelease is suspended
	if hr.Spec.Suspend {
		logger.Info("HelmRelease is suspended, checking if we need to unsuspend")

		// Get the list of Helm release secrets
		secrets, err := r.listHelmReleaseSecrets(ctx, hr.Namespace, hr.Name)
		if err != nil {
			logger.Error(err, "Failed to list Helm release secrets")
			return ctrl.Result{}, err
		}

		// If no secrets, treat latest version as 0
		latestVersion := 0
		if len(secrets) > 0 {
			latestSecret := getLatestSecret(secrets)
			latestVersion = extractVersionNumber(latestSecret.Name)
		} else {
			logger.Info("No Helm release secrets found while suspended, treating as version 0")
		}

		// Check if version is previous to just processed (latestVersion+1 == processedVersion)
		// This is the ONLY condition when we unsuspend
		shouldUnsuspend := false
		if hr.Annotations != nil {
			if processedVersionStr, exists := hr.Annotations[annotationLastProcessedVersion]; exists {
				processedVersion, err := strconv.Atoi(processedVersionStr)
				if err == nil && latestVersion+1 == processedVersion {
					shouldUnsuspend = true
				}
			}
		}

		if shouldUnsuspend {
			// Unsuspend the HelmRelease
			logger.Info("Secret was already deleted in previous run, removing suspend", "latest", latestVersion, "processed", latestVersion+1)
			if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
				logger.Info("Could not unsuspend HelmRelease, will retry on next reconcile", "error", err.Error())
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, nil
		}

		// If not previous to processed, skip all actions
		logger.Info("HelmRelease is suspended by external process, skipping", "latest", latestVersion)
		return ctrl.Result{}, nil
	}

	// Check if HelmRelease has the specific error
	if !hasNoDeployedReleasesError(hr) {
		logger.V(1).Info("HelmRelease does not have 'has no deployed releases' error, skipping")
		return ctrl.Result{}, nil
	}

	logger.Info("Detected HelmRelease with 'has no deployed releases' error")

	// Get the list of Helm release secrets
	secrets, err := r.listHelmReleaseSecrets(ctx, hr.Namespace, hr.Name)
	if err != nil {
		logger.Error(err, "Failed to list Helm release secrets")
		return ctrl.Result{}, err
	}

	if len(secrets) == 0 {
		logger.Info("No Helm release secrets found, skipping")
		return ctrl.Result{}, nil
	}

	// Find the latest version
	latestSecret := getLatestSecret(secrets)
	latestVersion := extractVersionNumber(latestSecret.Name)

	logger.Info("Found latest Helm release version", "version", latestVersion, "secret", latestSecret.Name)

	// Check if we just processed the next version (current + 1 == processed)
	if hr.Annotations != nil {
		if processedVersionStr, exists := hr.Annotations[annotationLastProcessedVersion]; exists {
			processedVersion, err := strconv.Atoi(processedVersionStr)
			if err == nil {
				if latestVersion+1 == processedVersion {
					logger.Info("Already processed, secret was deleted previously", "latest", latestVersion, "processed", processedVersion)
					return ctrl.Result{}, nil
				}
			} else {
				// Failed to parse annotation, treat as if annotation doesn't exist
				logger.Info("Failed to parse annotation, will process", "annotation", processedVersionStr, "error", err)
			}
		}
	}

	// Suspend the HelmRelease
	logger.Info("Suspending HelmRelease")
	if err := r.suspendHelmRelease(ctx, hr); err != nil {
		// Optimistic lock conflicts are normal - FluxCD also updates HelmRelease
		// Don't return error, just log and let controller-runtime requeue on next update
		logger.Info("Could not suspend HelmRelease, will retry on next reconcile", "error", err.Error())
		return ctrl.Result{}, nil
	}

	// Delete the latest secret
	logger.Info("Deleting latest Helm release secret", "secret", latestSecret.Name)
	if err := r.Delete(ctx, &latestSecret); err != nil {
		logger.Error(err, "Failed to delete Helm release secret")
		return ctrl.Result{}, err
	}

	// Update annotation with processed version
	logger.Info("Updating annotation with processed version", "version", latestVersion)
	if err := r.updateProcessedVersionAnnotation(ctx, hr, latestVersion); err != nil {
		logger.Info("Could not update annotation, will retry on next reconcile", "error", err.Error())
		return ctrl.Result{}, nil
	}

	// Unsuspend the HelmRelease
	logger.Info("Unsuspending HelmRelease")
	if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
		logger.Info("Could not unsuspend HelmRelease, will retry on next reconcile", "error", err.Error())
		return ctrl.Result{}, nil
	}

	logger.Info("Successfully processed HelmRelease", "version", latestVersion)
	return ctrl.Result{}, nil
}

// hasNoDeployedReleasesError checks if the HelmRelease has the specific error
func hasNoDeployedReleasesError(hr *helmv2.HelmRelease) bool {
	for _, condition := range hr.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionFalse {
			if strings.Contains(condition.Message, errorMessageNoDeployedReleases) {
				return true
			}
		}
	}
	return false
}

// listHelmReleaseSecrets lists all Helm release secrets for a specific release
func (r *FluxPlunger) listHelmReleaseSecrets(ctx context.Context, namespace, releaseName string) ([]corev1.Secret, error) {
	secretList := &corev1.SecretList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{
			"name":  releaseName,
			"owner": "helm",
		},
	}

	if err := r.List(ctx, secretList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	// Filter only helm.sh/release.v1 secrets
	filtered := []corev1.Secret{}
	for _, secret := range secretList.Items {
		if secret.Type == "helm.sh/release.v1" {
			filtered = append(filtered, secret)
		}
	}

	return filtered, nil
}

// getLatestSecret returns the secret with the highest version number
func getLatestSecret(secrets []corev1.Secret) corev1.Secret {
	if len(secrets) == 1 {
		return secrets[0]
	}

	sort.Slice(secrets, func(i, j int) bool {
		vi := extractVersionNumber(secrets[i].Name)
		vj := extractVersionNumber(secrets[j].Name)
		return vi > vj
	})

	return secrets[0]
}

// extractVersionFromSecretName extracts version string from secret name
// e.g., "sh.helm.release.v1.cozystack-resource-definitions.v10" -> "v10"
func extractVersionFromSecretName(secretName string) string {
	parts := strings.Split(secretName, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// extractVersionNumber extracts numeric version from secret name
// e.g., "sh.helm.release.v1.cozystack-resource-definitions.v10" -> 10
func extractVersionNumber(secretName string) int {
	version := extractVersionFromSecretName(secretName)
	// Remove 'v' prefix if present
	version = strings.TrimPrefix(version, "v")
	num, err := strconv.Atoi(version)
	if err != nil {
		return 0
	}
	return num
}

// suspendHelmRelease sets suspend to true on the HelmRelease
func (r *FluxPlunger) suspendHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) error {
	// Re-fetch the HelmRelease to get the latest state
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.Get(ctx, key, latestHR); err != nil {
		return fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}

	// If already suspended, nothing to do
	if latestHR.Spec.Suspend {
		return nil
	}

	patch := client.MergeFromWithOptions(latestHR.DeepCopy(), client.MergeFromWithOptimisticLock{})
	latestHR.Spec.Suspend = true

	return r.Patch(ctx, latestHR, patch, client.FieldOwner(fieldManager))
}

// unsuspendHelmRelease sets suspend to false on the HelmRelease
func (r *FluxPlunger) unsuspendHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) error {
	// Re-fetch the HelmRelease to get the latest state
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.Get(ctx, key, latestHR); err != nil {
		return fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}

	// If already unsuspended, nothing to do
	if !latestHR.Spec.Suspend {
		return nil
	}

	patch := client.MergeFromWithOptions(latestHR.DeepCopy(), client.MergeFromWithOptimisticLock{})
	latestHR.Spec.Suspend = false

	return r.Patch(ctx, latestHR, patch, client.FieldOwner(fieldManager))
}

// updateProcessedVersionAnnotation updates the annotation with the processed version
func (r *FluxPlunger) updateProcessedVersionAnnotation(ctx context.Context, hr *helmv2.HelmRelease, version int) error {
	// Re-fetch the HelmRelease to get the latest state
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.Get(ctx, key, latestHR); err != nil {
		return fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}

	patch := client.MergeFromWithOptions(latestHR.DeepCopy(), client.MergeFromWithOptimisticLock{})

	if latestHR.Annotations == nil {
		latestHR.Annotations = make(map[string]string)
	}
	latestHR.Annotations[annotationLastProcessedVersion] = strconv.Itoa(version)

	return r.Patch(ctx, latestHR, patch, client.FieldOwner(fieldManager))
}

// SetupWithManager sets up the controller with the Manager
func (r *FluxPlunger) SetupWithManager(mgr ctrl.Manager) error {
	// Watch HelmReleases that either:
	// 1. Have the specific error, OR
	// 2. Are suspended with our annotation (to handle crash recovery)
	pred := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		hr, ok := obj.(*helmv2.HelmRelease)
		if !ok {
			return false
		}

		// Always process if has error
		if hasNoDeployedReleasesError(hr) {
			return true
		}

		// Also process suspended HelmReleases with our annotation (crash recovery)
		if hr.Spec.Suspend && hr.Annotations != nil {
			if _, exists := hr.Annotations[annotationLastProcessedVersion]; exists {
				return true
			}
		}

		return false
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("fluxplunger").
		For(&helmv2.HelmRelease{}).
		WithEventFilter(pred).
		Complete(r)
}
