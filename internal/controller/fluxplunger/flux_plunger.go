package fluxplunger

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	//
	// This is a deliberate early, explicit statement of that invariant, not the only
	// thing enforcing it: an owned suspension on a terminating release would also be
	// cleared by the suspend branch below, and the recovery path is kept off a
	// terminating object anyway by suspendHelmRelease refusing to acquire one (and by
	// the releaseIsTerminating re-check before the Secret delete). Removing this
	// block would not change the outcome of any single case — its value is handling
	// deletion first and in one place, defense-in-depth with those downstream guards.
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
		// recovery crashed mid-flight rather than another instance running now: the
		// Deployment pins replicas: 1 and a rollout that never runs a second pod
		// (packages/system/flux-plunger, RollingUpdate with maxSurge 0 — see the
		// comment there for why a surge pod would break this annotation-as-state), and
		// controller-runtime serialises reconciles per key within that one process,
		// so there is no concurrent owner to race. Releasing it un-sticks the
		// release and lets error-recovery start over. A suspension without the marker
		// belongs to an operator or another controller and must be left untouched —
		// the processed-version annotation is deliberately NOT consulted here, because
		// it is never cleared and would misread a much later, unrelated operator
		// suspension as ours once the release's latest revision stops advancing.
		// This caller-side gate is an early-out; its safety is backstopped by
		// unsuspendHelmRelease's own re-checked ownership guard (which a test pins),
		// so it cannot be isolated by a Reconcile-level test and dropping it would
		// still not clear an unowned suspension.
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

	// A suspension flux-plunger set is marked with its ownership annotation and
	// cleared atomically with it. But an external actor — an operator, `cozyhr
	// resume`, the backup controller's own unsuspend — can lift the suspension by
	// writing spec.suspend=false WITHOUT removing the marker, since nothing else
	// removes it. A marker left on a non-suspended release would make a LATER,
	// unrelated operator suspension look plunger-owned and get cleared out from
	// under them: the one boundary this controller must never cross. Reap the stale
	// marker the moment we observe it on a release that is no longer suspended,
	// before it can be inherited. The watch predicate wakes us for this transition.
	if ownsSuspension(hr) {
		logger.Info("Reaping stale flux-plunger ownership marker on a non-suspended HelmRelease")
		if err := r.clearOwnershipMarker(ctx, hr); err != nil {
			logger.Info("Could not reap stale ownership marker, requeuing", "error", err.Error())
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
		// An optimistic-lock conflict is expected here — helm-controller and Flux
		// also patch the HelmRelease — and the conflicting write is itself an update
		// event that requeues us, so a conflict needs no error return. Any OTHER
		// error (a transient API failure) produces no such follow-up event, so
		// return it to requeue with backoff, symmetric with every step below;
		// swallowing it would strand a stuck release until the next informer resync.
		if apierrors.IsConflict(err) {
			logger.Info("Suspend conflicted with a concurrent write, will retry on the resulting update", "error", err.Error())
			return ctrl.Result{}, nil
		}
		logger.Info("Could not suspend HelmRelease, requeuing", "error", err.Error())
		return ctrl.Result{}, err
	}
	if !acquired {
		// The release was suspended by someone else, or started terminating, between
		// our read and our write, so flux-plunger does not own it. Do not delete its
		// storage Secret; let the next reconcile handle it (the suspended branch, or
		// the deletion branch which lets helm-controller uninstall).
		logger.Info("HelmRelease was not acquired for recovery, deferring to next reconcile")
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

	// Record the version we are about to reclaim BEFORE deleting its storage
	// Secret. The "already processed" guard above keys on this annotation, so
	// writing it first means a failed write can never authorise deleting the NEXT
	// revision on the following requeue: on failure we return here, the deferred
	// unsuspend clears the suspension, and the retry re-deletes the SAME revision
	// (idempotent) instead of walking down the release history one Secret per
	// requeue until none is left — which would itself orphan the release. Requeue
	// with backoff on failure, symmetric with every other step.
	logger.Info("Updating annotation with processed version", "version", latestVersion)
	if err := r.updateProcessedVersionAnnotation(ctx, hr, latestVersion); err != nil {
		logger.Info("Could not update annotation, requeuing", "error", err.Error())
		return ctrl.Result{}, err
	}

	// A delete can land between acquiring the suspension and here. Deleting the
	// storage Secret of a terminating release removes the only revision
	// helm-controller could uninstall; it then treats the release as already gone,
	// drops its finalizer, and orphans the resources — the exact failure this
	// recovery exists to prevent, reached by a narrower race than the suspended one.
	// The top-of-Reconcile check and suspendHelmRelease's re-fetch only see a delete
	// visible earlier, so re-check immediately before the delete. On termination,
	// leave the Secret intact and return: the deferred unsuspend clears our
	// suspension so helm-controller runs the uninstall against the intact revision.
	// This narrows the window to the Get-then-Delete gap but cannot fully close it —
	// there is no HelmRelease+Secret transaction — so a delete landing in that gap is
	// an accepted residual, the tightest a two-object check-then-act allows.
	terminating, err := r.releaseIsTerminating(ctx, hr)
	if err != nil {
		logger.Info("Could not re-check deletion before deleting the storage Secret, requeuing", "error", err.Error())
		return ctrl.Result{}, err
	}
	if terminating {
		// Clear our suspension explicitly and requeue on failure. Leaving it to the
		// log-only deferred fallback would let a failed unsuspend strand an owned
		// suspension on a terminating release with nothing retrying it — the exact
		// state that makes helm-controller skip the uninstall and orphan the
		// resources. The Secret is left intact so the uninstall has a revision.
		logger.Info("HelmRelease started terminating during recovery, clearing suspension so helm-controller can uninstall")
		if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
			logger.Info("Could not clear suspension on terminating release, requeuing", "error", err.Error())
			return ctrl.Result{}, err
		}
		completed = true
		return ctrl.Result{}, nil
	}

	// Delete the latest secret
	logger.Info("Deleting latest Helm release secret", "secret", latestSecret.Name)
	if err := r.Delete(ctx, &latestSecret); err != nil {
		logger.Error(err, "Failed to delete Helm release secret")
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
//
// The marker is a bare boolean with no identity token, which bounds what it can
// prove. It is written and cleared atomically with spec.suspend, and the two
// reachable ways it can outlive its suspension are both handled: an external resume
// that leaves it behind is reaped (predicate wake + clearOwnershipMarker), and a
// reap racing a concurrent operator suspend still strips the stale marker without
// clearing that suspend. The one residual it cannot distinguish is a full
// resume-then-suspend by external actors landing inside a single in-flight recovery
// reconcile (between suspendHelmRelease and the final unsuspend): the final
// unsuspend would then clear that new, unrelated suspension. That window is a few
// API round trips wide on one release being actively recovered, and closing it
// needs an identity-stamped marker (a generation/UID token) — deferred because it
// is not verifiable against the controller-runtime fake client, which does not
// track metadata.generation, and because keying ownership on generation would
// disown a legitimate crash-recovery suspension whose spec was touched during the
// crash window.
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
// suspension: false means the release was already suspended by someone else, or
// it started terminating after the top-of-Reconcile check, so the caller must
// not proceed as if it owns it.
func (r *FluxPlunger) suspendHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) (bool, error) {
	// Re-fetch the HelmRelease to get the latest state
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.Get(ctx, key, latestHR); err != nil {
		return false, fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}

	// Never suspend a terminating release. A delete can arrive between the
	// top-of-Reconcile deletion check and this re-fetch, and suspending a release
	// helm-controller is about to uninstall is exactly what makes it skip the
	// uninstall and orphan the release's resources.
	if !latestHR.DeletionTimestamp.IsZero() {
		return false, nil
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

// releaseIsTerminating re-fetches the HelmRelease and reports whether it has begun
// terminating. Recovery calls this immediately before deleting the storage Secret
// so a delete that landed after the earlier checks cannot make it remove the only
// revision helm-controller could uninstall.
func (r *FluxPlunger) releaseIsTerminating(ctx context.Context, hr *helmv2.HelmRelease) (bool, error) {
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.Get(ctx, key, latestHR); err != nil {
		return false, fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}
	return !latestHR.DeletionTimestamp.IsZero(), nil
}

// clearOwnershipMarker removes a stale ownership marker. It is reached only from
// the reap branch, which runs when the top-of-Reconcile read saw the release NOT
// suspended: the marker there is always stale, because a suspension flux-plunger
// currently holds would read spec.suspend=true and be handled by the suspend
// branch, and one worker per key means no concurrent recovery is holding it. So the
// marker is removed UNCONDITIONALLY — including when an operator re-suspended the
// release between that read and this re-fetch: deleting the stale marker is exactly
// what stops that fresh, unrelated suspension from being misread as ours on the
// next reconcile. The patch carries no optimistic lock and names only the
// annotation key, so it removes the marker without clobbering, or losing a conflict
// to, a concurrent spec.suspend write — the operator's suspension is left intact,
// just no longer wearing our marker.
func (r *FluxPlunger) clearOwnershipMarker(ctx context.Context, hr *helmv2.HelmRelease) error {
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.Get(ctx, key, latestHR); err != nil {
		return fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}
	if _, ok := latestHR.Annotations[annotationSuspendedByPlunger]; !ok {
		return nil
	}
	patch := client.MergeFrom(latestHR.DeepCopy())
	delete(latestHR.Annotations, annotationSuspendedByPlunger)
	return r.Patch(ctx, latestHR, patch, client.FieldOwner(fieldManager))
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

	// If the release is already unsuspended, clear any stale ownership marker and
	// stop. Same operation, and same reasoning, as clearOwnershipMarker: removing
	// only the annotation key is safe regardless of a concurrent spec.suspend write,
	// so the patch carries no optimistic lock — under contention it must not fail and
	// leave the stale marker behind, since a leftover marker is exactly what a later
	// unrelated suspension could inherit.
	if !latestHR.Spec.Suspend {
		if _, ok := latestHR.Annotations[annotationSuspendedByPlunger]; !ok {
			return nil
		}
		patch := client.MergeFrom(latestHR.DeepCopy())
		delete(latestHR.Annotations, annotationSuspendedByPlunger)
		return r.Patch(ctx, latestHR, patch, client.FieldOwner(fieldManager))
	}

	// Deliberate redundancy with the caller-side ownership gate in Reconcile: this
	// re-checks against the freshly re-fetched object, which the caller cannot,
	// and it is the guard pinned directly by
	// TestUnsuspendHelmRelease_LeavesSuspensionWithoutMarker so a future refactor
	// that drops the caller-side gate still cannot clear an operator's suspension.
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
	// 2. Carry flux-plunger's ownership marker — whether still suspended (crash
	//    recovery, and a delete arriving mid-recovery, both need to wake a
	//    reconcile) or already resumed by an external actor (reap the stale marker
	//    before a later operator suspension can inherit it).
	pred := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		hr, ok := obj.(*helmv2.HelmRelease)
		if !ok {
			return false
		}

		// Always process if has error
		if hasNoDeployedReleasesError(hr) {
			return true
		}

		// Also process any HelmRelease still carrying our ownership marker.
		if ownsSuspension(hr) {
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
