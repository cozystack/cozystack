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
	// annotationSuspendedByPlunger marks a suspension that flux-plunger itself
	// set, so recovery can be told apart from a suspension an operator or another
	// controller owns. It is written atomically with spec.suspend=true and
	// removed atomically with spec.suspend=false.
	annotationSuspendedByPlunger   = "flux-plunger.cozystack.io/suspended"
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

	// A HelmRelease that is being deleted must never stay suspended. helm-controller
	// v1.5.x skips the Helm uninstall and drops its finalizer for a suspended
	// HelmRelease (https://github.com/fluxcd/helm-controller/blob/v1.5.0/internal/controller/helmrelease_controller.go#L447-L475),
	// which orphans every resource the release owns. If flux-plunger owns the
	// suspension, release it so the uninstall proceeds; in every case do not begin
	// recovery on a terminating object.
	if !hr.DeletionTimestamp.IsZero() {
		if hr.Spec.Suspend && ownsSuspension(hr) {
			logger.Info("HelmRelease is being deleted while suspended by flux-plunger, clearing suspension so uninstall can proceed")
			if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
				// Requeue with backoff: leaving the suspension is exactly what
				// orphans the release, so retry rather than wait for another event.
				logger.Info("Could not clear suspension on deleted HelmRelease, requeuing", "error", err.Error())
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Check if HelmRelease is suspended
	if hr.Spec.Suspend {
		// Ownership is decided solely by the marker flux-plunger writes atomically
		// with the suspension. A suspension we own but see in a fresh reconcile means
		// recovery crashed mid-flight (default reconcile concurrency is one worker per
		// key, so it cannot be another in-flight run), so releasing it un-sticks the
		// release and lets error-recovery start over. A suspension without the marker
		// belongs to an operator or another controller and must be left untouched —
		// the processed-version annotation is deliberately NOT consulted here, because
		// it is never cleared and would misread a much later, unrelated operator
		// suspension as ours once the release's latest revision stops advancing.
		if !ownsSuspension(hr) {
			logger.Info("HelmRelease is suspended by external process, skipping")
			return ctrl.Result{}, nil
		}

		logger.Info("Removing suspension owned by flux-plunger")
		if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
			// Requeue with backoff so a stuck owned suspension is not left until the
			// next informer resync, symmetric with the deletion and recovery paths.
			logger.Info("Could not unsuspend HelmRelease, requeuing", "error", err.Error())
			return ctrl.Result{}, err
		}
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
	acquired, err := r.suspendHelmRelease(ctx, hr)
	if err != nil {
		// Optimistic lock conflicts are normal - FluxCD also updates HelmRelease
		// Don't return error, just log and let controller-runtime requeue on next update
		logger.Info("Could not suspend HelmRelease, will retry on next reconcile", "error", err.Error())
		return ctrl.Result{}, nil
	}
	if !acquired {
		// The release was suspended concurrently between our read and our write, so
		// flux-plunger does not own this suspension. Do not delete its storage
		// Secret; let the next reconcile handle it through the suspended branch.
		logger.Info("HelmRelease became suspended concurrently, deferring to next reconcile")
		return ctrl.Result{}, nil
	}

	// From here flux-plunger owns the suspension (acquired above). Guarantee it is
	// released before returning from any path below: a step that fails must never
	// leave the HelmRelease suspended, because a delete arriving on a suspended
	// release makes helm-controller skip the uninstall and orphan the release's
	// resources. The unsuspend is idempotent, so the happy path running it
	// explicitly and this deferred fallback are safe together.
	completed := false
	defer func() {
		if completed {
			return
		}
		if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
			logger.Info("Could not clear suspension after failed recovery step, will retry on next reconcile", "error", err.Error())
		}
	}()

	// Delete the latest secret
	logger.Info("Deleting latest Helm release secret", "secret", latestSecret.Name)
	if err := r.Delete(ctx, &latestSecret); err != nil {
		logger.Error(err, "Failed to delete Helm release secret")
		return ctrl.Result{}, err
	}

	// Update annotation with processed version. This runs after the delete, so a
	// persistent failure here lets the next reconcile delete the following storage
	// revision too. That is the accepted cost of clearing the suspension on every
	// failed step: burning a revision is strictly better than staying suspended,
	// which is the orphan-on-delete hazard this recovery exists to avoid. Requeue
	// with backoff on failure, symmetric with the other steps: the deferred
	// unsuspend still clears the suspension, and the requeue guarantees a retry even
	// when the same transient cause also fails that deferred patch.
	logger.Info("Updating annotation with processed version", "version", latestVersion)
	if err := r.updateProcessedVersionAnnotation(ctx, hr, latestVersion); err != nil {
		logger.Info("Could not update annotation, requeuing", "error", err.Error())
		return ctrl.Result{}, err
	}

	// Unsuspend the HelmRelease. Force a backoff requeue on failure, symmetric with
	// the deletion path: a persistently failing unsuspend (e.g. an admission webhook
	// rejecting the patch) would otherwise leave the release suspended-by-plunger
	// until the informer's periodic resync, far longer than necessary.
	logger.Info("Unsuspending HelmRelease")
	if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
		logger.Info("Could not unsuspend HelmRelease, requeuing", "error", err.Error())
		// The returned error requeues and retries; skip the redundant deferred attempt.
		completed = true
		return ctrl.Result{}, err
	}

	completed = true
	logger.Info("Successfully processed HelmRelease", "version", latestVersion)
	return ctrl.Result{}, nil
}

// ownsSuspension reports whether the current suspension was set by flux-plunger,
// as opposed to an operator or another controller. Recovery and delete handling
// must only clear a suspension flux-plunger owns.
func ownsSuspension(hr *helmv2.HelmRelease) bool {
	return hr.Annotations[annotationSuspendedByPlunger] == "true"
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

// suspendHelmRelease sets suspend to true on the HelmRelease and marks the
// suspension as owned by flux-plunger. It reports whether it acquired the
// suspension: false means the release was already suspended by someone else, so
// the caller must not proceed as if it owns it.
func (r *FluxPlunger) suspendHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) (bool, error) {
	// Re-fetch the HelmRelease to get the latest state
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.Get(ctx, key, latestHR); err != nil {
		return false, fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}

	// If already suspended, do not take ownership of a suspension we did not set.
	if latestHR.Spec.Suspend {
		return false, nil
	}

	patch := client.MergeFromWithOptions(latestHR.DeepCopy(), client.MergeFromWithOptimisticLock{})
	latestHR.Spec.Suspend = true
	if latestHR.Annotations == nil {
		latestHR.Annotations = make(map[string]string)
	}
	latestHR.Annotations[annotationSuspendedByPlunger] = "true"

	if err := r.Patch(ctx, latestHR, patch, client.FieldOwner(fieldManager)); err != nil {
		return false, err
	}
	return true, nil
}

// unsuspendHelmRelease clears a flux-plunger-owned suspension and its marker.
// Callers already gate on ownership; the re-fetched ownership check here is
// defense-in-depth against a future caller that forgets to, so an operator's
// suspension is never cleared by accident.
func (r *FluxPlunger) unsuspendHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) error {
	// Re-fetch the HelmRelease to get the latest state
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.Get(ctx, key, latestHR); err != nil {
		return fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}

	// If already unsuspended, clear any stale ownership marker and stop.
	if !latestHR.Spec.Suspend {
		if _, ok := latestHR.Annotations[annotationSuspendedByPlunger]; !ok {
			return nil
		}
		patch := client.MergeFromWithOptions(latestHR.DeepCopy(), client.MergeFromWithOptimisticLock{})
		delete(latestHR.Annotations, annotationSuspendedByPlunger)
		return r.Patch(ctx, latestHR, patch, client.FieldOwner(fieldManager))
	}

	if !ownsSuspension(latestHR) {
		return nil
	}

	patch := client.MergeFromWithOptions(latestHR.DeepCopy(), client.MergeFromWithOptimisticLock{})
	latestHR.Spec.Suspend = false
	delete(latestHR.Annotations, annotationSuspendedByPlunger)

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
	// 2. Are suspended with our ownership marker (crash recovery and a delete
	//    arriving mid-recovery both need to wake a reconcile).
	pred := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		hr, ok := obj.(*helmv2.HelmRelease)
		if !ok {
			return false
		}

		// Always process if has error
		if hasNoDeployedReleasesError(hr) {
			return true
		}

		// Also process suspended HelmReleases flux-plunger owns.
		if hr.Spec.Suspend && ownsSuspension(hr) {
			return true
		}

		return false
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("fluxplunger").
		For(&helmv2.HelmRelease{}).
		WithEventFilter(pred).
		Complete(r)
}
