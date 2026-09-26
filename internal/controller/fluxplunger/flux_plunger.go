package fluxplunger

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	annotationLastProcessedVersion = "flux-plunger.cozystack.io/last-processed-version"
	errorMessageNoDeployedReleases = "has no deployed releases"
	// fieldManager owns the client-side patch of the processed-version annotation.
	fieldManager = "flux-client-side-apply"
	// suspendFieldManager is the server-side-apply field manager flux-plunger uses
	// to own spec.suspend. Ownership of a suspension is decided by whether this
	// manager owns the field in metadata.managedFields, not by an annotation: the
	// apiserver transfers the field away the moment another writer changes
	// spec.suspend (a resume, then a re-suspend), so a later operator suspension is
	// never misread as flux-plunger's, while an unrelated re-apply of other fields
	// leaves the ownership untouched (no self-disown). A write of the value the field
	// already holds does not transfer it.
	suspendFieldManager = "flux-plunger"
	// fenceFinalizer holds a HelmRelease while flux-plunger's recovery has it
	// suspended. On a delete, helm-controller v1.5.x drops its own finalizer without
	// running the Helm uninstall if the release is suspended, and it wakes on the same
	// event flux-plunger does, so lifting the suspension first cannot be guaranteed.
	// This finalizer keeps the object until flux-plunger has lifted the suspension
	// and helm-controller has reconciled the unsuspended release, which runs the
	// uninstall. It is applied in the same server-side apply as the suspension and
	// removed with it once a recovery completes; on a delete it outlives the
	// suspension and is removed only once helm-controller has handled the
	// unsuspended release (uninstallReached).
	fenceFinalizer = "flux-plunger.cozystack.io/uninstall-fence"

	// fenceRecheckInterval is how often a delete held by the fence is re-checked
	// without waiting for an event, so a stalled helm-controller is noticed.
	fenceRecheckInterval = time.Minute
	// fenceHeldWarnAfter is how long a delete may be held by the fence, waiting for
	// helm-controller to handle the unsuspended release, before it is raised as a
	// Warning: an uninstall normally lands within seconds.
	fenceHeldWarnAfter = 10 * time.Minute
)

// FluxPlunger watches HelmRelease resources and fixes "has no deployed releases" errors
type FluxPlunger struct {
	client.Client

	// APIReader reads directly from the apiserver, bypassing the informer cache.
	// The re-fetch guards below (already-suspended, terminating) must not read the
	// cache: it lags a write or delete the apiserver has already accepted, which
	// would defeat the pre-delete termination re-check whose whole purpose is to
	// catch a just-arrived delete. Production wires mgr.GetAPIReader(); when nil
	// (tests), refetch falls back to the cached client.
	APIReader client.Reader

	// Recorder emits Events so an operator can see a release flux-plunger declined
	// to act on (an externally-owned suspension) rather than reading it out of the
	// log. Production wires mgr.GetEventRecorderFor; nil is tolerated (tests).
	Recorder record.EventRecorder

	// owns overrides the suspension-ownership check. Production leaves it nil and
	// uses ownsSuspensionManagedFields (the managedFields-based check, which the
	// controller-runtime fake client cannot represent). Tests in this package
	// inject a stand-in so the branch, ordering and requeue behaviour can be
	// exercised on the fake client, while the managedFields check itself is pinned
	// by a unit test on hand-crafted managedFields and by the envtest suite.
	owns func(*helmv2.HelmRelease) bool

	// now overrides the clock for tests; production leaves it nil.
	now func() time.Time
}

func (r *FluxPlunger) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// recordEvent emits an Event when a Recorder is wired. The message is static so
// the recorder's event correlator folds repeats into a single counted Event rather
// than a flood.
func (r *FluxPlunger) recordEvent(hr *helmv2.HelmRelease, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(hr, eventType, reason, message)
	}
}

// recordUnsuspendFailed raises a Warning when flux-plunger holds a suspension and
// could not clear it: the state in which a delete makes helm-controller skip the
// uninstall and orphan the release, which an operator can see in
// `kubectl describe helmrelease` and must be able to alert on. A conflict is not
// such a failure: the apply is preconditioned on what was read, so any write in
// between (helm-controller's status update included) rejects it, and the requeue
// retries it against the new state.
func (r *FluxPlunger) recordUnsuspendFailed(hr *helmv2.HelmRelease, err error) {
	if apierrors.IsConflict(err) {
		return
	}
	r.recordEvent(hr, corev1.EventTypeWarning, "UnsuspendFailed",
		"Could not clear a suspension flux-plunger set; retrying. While it stays suspended, deleting the release skips the Helm uninstall and orphans its resources")
}

// uninstallReached reports whether helm-controller has handled a terminating
// release in its current, unsuspended form: its finalizer is gone and it has
// observed the latest generation, which lifting the suspension bumped. Checking the
// finalizer alone is not enough, because helm-controller drops it without an
// uninstall while the release is still suspended.
func uninstallReached(hr *helmv2.HelmRelease) bool {
	return !controllerutil.ContainsFinalizer(hr, helmv2.HelmReleaseFinalizer) &&
		hr.Status.ObservedGeneration == hr.Generation
}

// ownsSuspension reports whether flux-plunger owns the current suspension, through
// the injected check in tests or the managedFields check in production.
func (r *FluxPlunger) ownsSuspension(hr *helmv2.HelmRelease) bool {
	if r.owns != nil {
		return r.owns(hr)
	}
	return ownsSuspensionManagedFields(hr)
}

// refetch reads the freshest state of a HelmRelease. It uses the uncached
// APIReader when wired (production) so an already-suspended or termination
// re-check sees a write or delete the apiserver has just accepted; it falls back
// to the cached client only when no APIReader is set (tests).
func (r *FluxPlunger) refetch(ctx context.Context, key types.NamespacedName, hr *helmv2.HelmRelease) error {
	if r.APIReader != nil {
		return r.APIReader.Get(ctx, key, hr)
	}
	return r.Get(ctx, key, hr)
}

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles HelmRelease resources with "has no deployed releases" error
func (r *FluxPlunger) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Read the HelmRelease from the apiserver, not the informer cache: the
	// already-processed guard below compares its processed-version annotation with a
	// Secret list that is itself read from the apiserver, and a cache that has not yet
	// seen the annotation written by the previous recovery would fail that guard and
	// delete the next revision down.
	hr := &helmv2.HelmRelease{}
	if err := r.refetch(ctx, req.NamespacedName, hr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !hr.DeletionTimestamp.IsZero() {
		return r.reconcileTerminating(ctx, hr)
	}

	// Check if HelmRelease is suspended
	if hr.Spec.Suspend {
		// Ownership is decided by which field manager owns spec.suspend (see
		// ownsSuspension). A suspension we own but see in a fresh reconcile means
		// recovery crashed mid-flight rather than another instance running now: the
		// Deployment pins replicas: 1 and a rollout that never runs a second pod
		// (packages/system/flux-plunger, RollingUpdate with maxSurge 0 — see the
		// comment there for why a surge pod would break this), and controller-runtime
		// serialises reconciles per key within that one process, so there is no
		// concurrent owner to race. Releasing it un-sticks the release and lets
		// error-recovery start over. A suspension flux-plunger does not own belongs to
		// an operator or another controller and must be left untouched.
		if !r.ownsSuspension(hr) {
			logger.Info("HelmRelease is suspended by external process, skipping")
			r.recordEvent(hr, corev1.EventTypeNormal, "SuspensionNotOwned",
				"Left an externally-owned suspension untouched; flux-plunger only clears suspensions it set")
			// Our fence only guards a suspension flux-plunger holds; with the
			// suspension taken over, do not keep the release from being deleted.
			return ctrl.Result{}, r.releaseFence(ctx, hr)
		}

		logger.Info("Removing suspension owned by flux-plunger")
		if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
			// Requeue with backoff so a stuck owned suspension is not left until the
			// next informer resync, symmetric with the deletion and recovery paths.
			logger.Info("Could not unsuspend HelmRelease, requeuing", "error", err.Error())
			r.recordUnsuspendFailed(hr, err)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// A fence without a suspension is left when another writer (an operator,
	// `cozyhr resume`) set spec.suspend=false and so took the field from
	// flux-plunger; it guards nothing any more.
	if controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		return ctrl.Result{}, r.releaseFence(ctx, hr)
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
		// Requeue with backoff on any error, symmetric with every step below.
		// suspendHelmRelease reports every conflict, on either apply, as not acquired
		// (handled below), so an error reaching here is a non-conflict failure (an
		// admission webhook rejecting the apply, or an API error) that produces no
		// follow-up event. Swallowing it would strand a stuck release until the next
		// informer resync.
		logger.Info("Could not suspend HelmRelease, requeuing", "error", err.Error())
		return ctrl.Result{}, err
	}
	if !acquired {
		// The release was suspended by someone else, started terminating, or was
		// written again between our read and our write, so flux-plunger does not own
		// it. Do not delete its storage Secret; the write that got in between is an
		// update event, and the next reconcile handles the release as it now is.
		logger.Info("HelmRelease was not acquired for recovery, deferring to next reconcile")
		r.recordEvent(hr, corev1.EventTypeNormal, "RecoveryDeferred",
			"Deferred recovery: spec.suspend was taken by another manager or the release began terminating between read and write; will retry on the next reconcile")
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
			r.recordUnsuspendFailed(hr, err)
		}
	}()

	// Record the version we are about to reclaim BEFORE deleting its storage
	// Secret. The "already processed" guard above keys on this annotation, so
	// writing it first means a failed write can never authorise deleting the NEXT
	// revision on the following requeue: on failure we return here, the deferred
	// unsuspend clears the suspension, and the retry re-deletes the SAME revision
	// (idempotent) instead of walking down the release history one Secret per
	// requeue until none is left, which would itself orphan the release. That covers
	// a failed write or delete only: the guard compares revision numbers, so it cannot
	// tell them from a revision Helm re-creates under the same number after the
	// delete, and such a revision is reclaimed again. Requeue with backoff on failure,
	// symmetric with every other step.
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
			r.recordUnsuspendFailed(hr, err)
			// The returned error requeues and retries; skip the redundant deferred attempt.
			completed = true
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
		r.recordUnsuspendFailed(hr, err)
		// The returned error requeues and retries; skip the redundant deferred attempt.
		completed = true
		return ctrl.Result{}, err
	}

	completed = true
	logger.Info("Successfully processed HelmRelease", "version", latestVersion)
	return ctrl.Result{}, nil
}

// reconcileTerminating makes a delete of a release flux-plunger is recovering reach
// the Helm uninstall. helm-controller v1.5.x skips the uninstall and drops its
// finalizer for a suspended HelmRelease
// (https://github.com/fluxcd/helm-controller/blob/v1.5.0/internal/controller/helmrelease_controller.go#L447-L475),
// and it handles the delete on the same event flux-plunger does. The fence
// finalizer keeps the object whichever handles it first; flux-plunger lifts its own
// suspension, which bumps the generation and makes helm-controller reconcile the
// unsuspended release and uninstall it, and only then lifts the fence. A terminating
// release never begins recovery.
func (r *FluxPlunger) reconcileTerminating(ctx context.Context, hr *helmv2.HelmRelease) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if hr.Spec.Suspend && r.ownsSuspension(hr) {
		logger.Info("HelmRelease is being deleted while suspended by flux-plunger, clearing suspension so uninstall can proceed")
		if err := r.unsuspendHelmRelease(ctx, hr); err != nil {
			logger.Info("Could not clear suspension on deleted HelmRelease, requeuing", "error", err.Error())
			r.recordUnsuspendFailed(hr, err)
			return ctrl.Result{}, err
		}
		// helm-controller's reconcile of the new generation updates the status,
		// which wakes this controller again to lift the fence.
		return ctrl.Result{}, nil
	}

	if hr.Spec.Suspend {
		// Another manager's suspension: helm-controller skips the uninstall for it
		// and leaves the release's resources in place, which a migration that
		// suspends then deletes relies on. That is not flux-plunger's decision to
		// override, and not a release its fence should hold. A release that
		// carries flux-plunger's processed-version annotation was recovered before,
		// so the suspension that skips its uninstall was not a migration's choice (an
		// operator's hand, or one an earlier flux-plunger release left behind): that
		// is the orphaning this controller exists to prevent, and a Warning.
		logger.Info("HelmRelease is being deleted while suspended by another manager; helm-controller skips the uninstall")
		if _, recovered := hr.Annotations[annotationLastProcessedVersion]; recovered {
			r.recordEvent(hr, corev1.EventTypeWarning, "TerminatingWhileSuspended",
				"HelmRelease is being deleted while suspended by another manager; helm-controller skips the Helm uninstall for a suspended release, so its resources are left behind")
		} else {
			r.recordEvent(hr, corev1.EventTypeNormal, "TerminatingWhileSuspended",
				"HelmRelease is being deleted while suspended by another manager; helm-controller skips the Helm uninstall for a suspended release and leaves its resources in place")
		}
		return ctrl.Result{}, r.releaseFence(ctx, hr)
	}

	if !controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		return ctrl.Result{}, nil
	}
	if uninstallReached(hr) {
		logger.Info("helm-controller has handled the unsuspended release, lifting the uninstall fence")
		return ctrl.Result{}, r.releaseFence(ctx, hr)
	}
	// Still waiting for helm-controller to handle the unsuspended release. Re-check
	// on a timer rather than only on its next status write, which never comes if it
	// is not running, and say so once the delete has been held past the deadline.
	if held := r.clock().Sub(hr.DeletionTimestamp.Time); held >= fenceHeldWarnAfter {
		logger.Info("HelmRelease delete is still held by the uninstall fence, waiting for helm-controller", "held", held.String())
		r.recordEvent(hr, corev1.EventTypeWarning, "UninstallFenceHeld",
			"HelmRelease delete is held by flux-plunger's uninstall fence until helm-controller handles the unsuspended release; check that helm-controller is running and reconciling it")
	}
	return ctrl.Result{RequeueAfter: fenceRecheckInterval}, nil
}

// releaseFence removes flux-plunger's fence finalizer by relinquishing it. Callers
// hold no suspension of their own at this point: the relinquishing apply drops
// every field flux-plunger's manager owns.
func (r *FluxPlunger) releaseFence(ctx context.Context, hr *helmv2.HelmRelease) error {
	if !controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		return nil
	}
	return r.applyRecoveryState(ctx, hr, nil, false, false)
}

// ownsSuspensionManagedFields reports whether flux-plunger owns the spec.suspend
// field via its server-side-apply field manager, recorded by the apiserver in
// metadata.managedFields. This is the sole ownership signal, and it is exact in
// the ways a bare annotation marker was not:
//
//   - An operator (or `cozyhr resume`, or the backup controller) that resumes and
//     later re-suspends the release writes spec.suspend under its own manager, so
//     the apiserver transfers the field away from flux-plunger and this returns
//     false — the later suspension is never misread as ours and cleared out from
//     under them, the one boundary this controller must never cross.
//   - An unrelated re-apply of other spec fields (the routine reconcile of the
//     cozystack operator or helm-controller) leaves spec.suspend's owner untouched,
//     so flux-plunger keeps ownership of a suspension it is mid-recovery on — no
//     self-disown, unlike keying ownership on metadata.generation.
func ownsSuspensionManagedFields(hr *helmv2.HelmRelease) bool {
	for _, e := range hr.ManagedFields {
		if e.Manager == suspendFieldManager &&
			e.Operation == metav1.ManagedFieldsOperationApply &&
			fieldsManageSuspend(e.FieldsV1) {
			return true
		}
	}
	return false
}

// fieldsManageSuspend reports whether a managed-fields set includes spec.suspend,
// encoded by the apiserver as {"f:spec":{"f:suspend":{}}}.
func fieldsManageSuspend(fs *metav1.FieldsV1) bool {
	if fs == nil || len(fs.Raw) == 0 {
		return false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(fs.Raw, &top); err != nil {
		return false
	}
	specRaw, ok := top["f:spec"]
	if !ok {
		return false
	}
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return false
	}
	_, ok = spec["f:suspend"]
	return ok
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

// listHelmReleaseSecrets lists all Helm release secrets for a specific release.
//
// It reads through the uncached APIReader (when wired). This is not only a race
// guard but a correctness fix: the controller's cached client (mgr.GetClient())
// would serve this List from an informer, and the ClusterRole grants secrets only
// get/list, NOT watch, which an informer needs to stay synced — so a cached List
// cannot reliably reflect the cluster (stale at best, unsynced at worst, with a
// steady stream of watch-forbidden errors). Reading directly from the apiserver via
// the APIReader needs only get/list and sidesteps the informer entirely.
// getLatestSecret over the result also chooses which storage Secret is deleted and
// which version the processed annotation records, and this List runs before the
// release is suspended, exactly when helm-controller may still be creating a
// revision Secret — the same race the other re-reads guard against — so the direct
// read is doubly warranted. flux-plunger deliberately does NOT cache Secrets (no
// watch RBAC, and it would mean watching every Secret in the cluster).
func (r *FluxPlunger) listHelmReleaseSecrets(ctx context.Context, namespace, releaseName string) ([]corev1.Secret, error) {
	secretList := &corev1.SecretList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{
			"name":  releaseName,
			"owner": "helm",
		},
	}

	lister := client.Reader(r.Client)
	if r.APIReader != nil {
		lister = r.APIReader
	}
	if err := lister.List(ctx, secretList, listOpts...); err != nil {
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

// applyRecoveryState server-side-applies the fields flux-plunger's field manager
// owns during a recovery: spec.suspend and the fence finalizer, and nothing else.
// Whatever the body leaves out is relinquished. value == nil omits spec.suspend
// (when flux-plunger is its sole manager the field reverts to the unsuspended
// default); value != nil sets it to *value. fence declares the fence finalizer;
// metadata.finalizers is a set, so this adds or drops only flux-plunger's entry and
// leaves helm-controller's alone. force appends client.ForceOwnership.
//
// The apply body is an unstructured object carrying just metadata and these
// fields, NOT a typed HelmReleaseSpec: marshaling a typed spec would also emit
// spec.interval ("0s", it is a required field without omitempty), and applying
// that over a release whose interval another manager owns conflicts on
// spec.interval and never suspends, while forcing would reset the release's
// reconcile interval.
func (r *FluxPlunger) applyRecoveryState(ctx context.Context, hr *helmv2.HelmRelease, value *bool, fence, force bool) error {
	apply := &unstructured.Unstructured{}
	apply.SetGroupVersionKind(helmv2.GroupVersion.WithKind("HelmRelease"))
	apply.SetNamespace(hr.Namespace)
	apply.SetName(hr.Name)
	// Precondition every apply on the object the caller read, so a write or delete
	// that lands after that read is rejected with a conflict and the decision is
	// taken again on the new state. The resourceVersion covers a change: unforced,
	// the apiserver would otherwise accept true over a foreign true as shared
	// ownership; forced, take the field over; and a relinquish decided on a live
	// release would remove the fence after helm-controller had already dropped its
	// finalizer on a delete, deleting the release without an uninstall. The uid
	// covers a release that is gone: server-side apply creates what does not exist,
	// and only a uid mismatch stops it resurrecting an empty HelmRelease.
	apply.SetResourceVersion(hr.ResourceVersion)
	apply.SetUID(hr.UID)
	if fence {
		apply.SetFinalizers([]string{fenceFinalizer})
	}
	if value != nil {
		if err := unstructured.SetNestedField(apply.Object, *value, "spec", "suspend"); err != nil {
			return err
		}
	}
	opts := []client.PatchOption{client.FieldOwner(suspendFieldManager)}
	if force {
		opts = append(opts, client.ForceOwnership)
	}
	return r.Patch(ctx, apply, client.Apply, opts...)
}

// suspendHelmRelease takes ownership of a fresh suspension via server-side apply.
// It reports whether it acquired one: false means the release was already
// suspended by someone else, or it started terminating after the top-of-Reconcile
// check, so the caller must not proceed as if it owns it.
//
// Both applies set the fence finalizer together with the suspension, so there is
// no moment at which flux-plunger holds a suspension a delete could slip past, and
// both are preconditioned on the resourceVersion of the read that decided on them
// (see applyRecoveryState), so any write landing after that read makes the apply
// fail with a conflict rather than be absorbed.
//
// The apply is attempted WITHOUT force first. A fresh, unowned spec.suspend applies
// cleanly and flux-plunger owns it. A conflict means the release changed since the
// read, or another manager already owns the field: this repo's backup controllers
// set spec.suspend via a dynamic-client Update (setCNPGRestoreHRSuspended /
// setEtcdRestoreHRSuspended / velero's suspendHelmRelease), and earlier flux-plunger
// releases owned it via a merge Patch, so a non-forced apply would 409 forever and
// flux-plunger could never recover such a release. On conflict we re-fetch and force
// ONLY if spec.suspend is still false: a manager owning the field at false is stale
// ownership safe to take over, while a value of true is an ACTIVE suspension (an
// operator's, or a backup mid-restore) that flux-plunger must never clear. A
// conflict on the forced retry means yet another write landed after the re-read,
// the same race as a foreign suspension seen on the re-read, so it is reported the
// same way: not acquired, no error.
func (r *FluxPlunger) suspendHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) (bool, error) {
	// Re-fetch the HelmRelease to get the latest state
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.refetch(ctx, key, latestHR); err != nil {
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

	suspendTrue := true
	err := r.applyRecoveryState(ctx, latestHR, &suspendTrue, true, false)
	if err == nil {
		return true, nil
	}
	if !apierrors.IsConflict(err) {
		return false, err
	}

	// Conflict: another manager owns spec.suspend. Re-read its value and force only
	// if it is still unsuspended (stale ownership); leave an active suspension alone.
	// Decode into a FRESH object: client-go's Get unmarshals into the receiver without
	// zeroing it, so a field absent from the second response (spec.suspend is
	// omitempty) would otherwise retain the first read's value.
	rechecked := &helmv2.HelmRelease{}
	if err := r.refetch(ctx, key, rechecked); err != nil {
		return false, fmt.Errorf("failed to re-fetch HelmRelease after suspend conflict: %w", err)
	}
	if !rechecked.DeletionTimestamp.IsZero() {
		return false, nil
	}
	if rechecked.Spec.Suspend {
		return false, nil
	}
	if err := r.applyRecoveryState(ctx, rechecked, &suspendTrue, true, true); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// releaseIsTerminating re-fetches the HelmRelease and reports whether it has begun
// terminating. Recovery calls this immediately before deleting the storage Secret
// so a delete that landed after the earlier checks cannot make it remove the only
// revision helm-controller could uninstall. A release that is already gone counts
// as terminating.
func (r *FluxPlunger) releaseIsTerminating(ctx context.Context, hr *helmv2.HelmRelease) (bool, error) {
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.refetch(ctx, key, latestHR); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}
	return !latestHR.DeletionTimestamp.IsZero(), nil
}

// unsuspendHelmRelease clears a suspension flux-plunger owns by relinquishing the
// spec.suspend field: a server-side apply that no longer declares it, which reverts
// the field to unsuspended (flux-plunger being its only manager) and drops
// flux-plunger's ownership. On a live release the fence goes with it. On a
// terminating one the fence stays: helm-controller may already have dropped its
// finalizer without an uninstall, and the fence is what keeps the object until it
// reconciles the unsuspended release (reconcileTerminating lifts it then). A
// release that is already gone has nothing left to clear.
//
// Callers already gate on ownership; the re-fetched ownership check here is
// defense-in-depth against a future caller that forgets to, so an operator's
// suspension, for which the apiserver has already transferred the field away from
// flux-plunger, is never cleared by accident.
func (r *FluxPlunger) unsuspendHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) error {
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.refetch(ctx, key, latestHR); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}

	// Nothing to do if flux-plunger does not own the suspension: either it is
	// already unsuspended, or the field belongs to another manager now.
	if !r.ownsSuspension(latestHR) {
		return nil
	}

	keepFence := !latestHR.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(latestHR, fenceFinalizer)
	return r.applyRecoveryState(ctx, latestHR, nil, keepFence, false)
}

// updateProcessedVersionAnnotation updates the annotation with the processed version
func (r *FluxPlunger) updateProcessedVersionAnnotation(ctx context.Context, hr *helmv2.HelmRelease, version int) error {
	// Re-fetch the HelmRelease to get the latest state
	key := types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}
	latestHR := &helmv2.HelmRelease{}
	if err := r.refetch(ctx, key, latestHR); err != nil {
		return fmt.Errorf("failed to get latest HelmRelease: %w", err)
	}

	patch := client.MergeFromWithOptions(latestHR.DeepCopy(), client.MergeFromWithOptimisticLock{})

	if latestHR.Annotations == nil {
		latestHR.Annotations = make(map[string]string)
	}
	latestHR.Annotations[annotationLastProcessedVersion] = strconv.Itoa(version)

	return r.Patch(ctx, latestHR, patch, client.FieldOwner(fieldManager))
}

// watchPredicate selects the HelmReleases flux-plunger reconciles: those carrying
// the specific error, a suspension flux-plunger owns (crash recovery, and a delete
// arriving mid-recovery, both need to wake a reconcile), flux-plunger's fence, or
// any suspended release that is being deleted. Kept a named method so a test can
// pin it.
func (r *FluxPlunger) watchPredicate(obj client.Object) bool {
	hr, ok := obj.(*helmv2.HelmRelease)
	if !ok {
		return false
	}
	if hasNoDeployedReleasesError(hr) {
		return true
	}
	if hr.Spec.Suspend && r.ownsSuspension(hr) {
		return true
	}
	// The fence is lifted in response to helm-controller's status update after the
	// uninstall, which carries neither the error nor a flux-plunger suspension.
	if controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		return true
	}
	// A suspended release being deleted skips the Helm uninstall even when the
	// suspension is someone else's and the release is healthy: Reconcile has to see it
	// to record TerminatingWhileSuspended.
	if hr.Spec.Suspend && !hr.DeletionTimestamp.IsZero() {
		return true
	}
	return false
}

// SetupWithManager sets up the controller with the Manager
func (r *FluxPlunger) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("fluxplunger").
		For(&helmv2.HelmRelease{}).
		WithEventFilter(predicate.NewPredicateFuncs(r.watchPredicate)).
		Complete(r)
}
