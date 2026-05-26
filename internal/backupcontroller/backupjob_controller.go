package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// BackupJobReconciler reconciles BackupJob with a strategy from the
// strategy.backups.cozystack.io API group.
type BackupJobReconciler struct {
	client.Client
	dynamic.Interface
	meta.RESTMapper
	Scheme            *runtime.Scheme
	Recorder          record.EventRecorder
	CredentialsConfig BackupCredentialsConfig
}

func (r *BackupJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling BackupJob", "namespace", req.Namespace, "name", req.Name)

	j := &backupsv1alpha1.BackupJob{}
	err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, j)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("BackupJob not found, skipping")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get BackupJob")
		return ctrl.Result{}, err
	}

	// Skip terminal BackupJobs: a Succeeded/Failed run must not keep
	// projecting Secrets or re-running dispatch on every requeue, which
	// would otherwise materialise cozy-backups-creds in tenant namespaces
	// long after the BackupJob is done and pin needless work on the
	// apiserver.
	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded || j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		logger.V(1).Info("BackupJob already terminal, skipping", "phase", j.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Normalize ApplicationRef (default apiGroup if not specified)
	normalizedAppRef := NormalizeApplicationRef(j.Spec.ApplicationRef)

	// Resolve BackupClass first so we know whether this BackupJob even
	// targets a strategy this controller owns. Projecting credentials
	// before this point would (a) leak cozy-backups-creds into namespaces
	// that use third-party strategies and (b) terminally fail BackupJobs
	// with an unrelated pre-existing cozy-backups-creds (ownership guard
	// kicks in even though no platform strategy is involved).
	resolved, err := ResolveBackupClass(ctx, r.Client, j.Spec.BackupClassName, normalizedAppRef)
	if err != nil {
		// ErrNoMatchingStrategy is a documented configuration error
		// (e.g. tenant fires FoundationDB BackupJob against cozy-default,
		// which intentionally does not bind FDB). Mark Failed terminally
		// so the tenant sees the misbinding instead of an infinite
		// requeue loop with no status signal.
		if errors.Is(err, ErrNoMatchingStrategy) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("BackupClass %q does not bind a strategy for Kind %q; see docs/operations/backup-classes.md", j.Spec.BackupClassName, normalizedAppRef.Kind))
		}
		// BackupClass not found yet is transient on a fresh install:
		// during the bootstrap window the cozy-default BackupClass is
		// gated on a populated BucketClaim status. Surface a clear
		// Ready=False condition + requeue rather than spamming Error
		// logs on every backoff cycle, mirroring the projection
		// transient-vs-terminal split below.
		if apierrors.IsNotFound(err) {
			meta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "BackupClassNotFound",
				Message: fmt.Sprintf("BackupClass %q not found; the platform may still be bootstrapping", j.Spec.BackupClassName),
			})
			if updateErr := r.Status().Update(ctx, j); updateErr != nil {
				logger.Error(updateErr, "failed to update BackupJob status to BackupClassNotFound")
			}
			return ctrl.Result{RequeueAfter: CredentialsProjectionRequeue}, nil
		}
		logger.Error(err, "failed to resolve BackupClass", "backupClassName", j.Spec.BackupClassName)
		return ctrl.Result{}, err
	}

	strategyRef := resolved.StrategyRef

	// Validate strategyRef
	if strategyRef.APIGroup == nil {
		logger.V(1).Info("BackupJob resolved StrategyRef has nil APIGroup, skipping", "backupjob", j.Name)
		return ctrl.Result{}, nil
	}

	if *strategyRef.APIGroup != strategyv1alpha1.GroupVersion.Group {
		logger.V(1).Info("BackupJob resolved StrategyRef.APIGroup doesn't match, skipping",
			"backupjob", j.Name,
			"expected", strategyv1alpha1.GroupVersion.Group,
			"got", *strategyRef.APIGroup)
		return ctrl.Result{}, nil
	}

	// Reject unsupported Kinds inside the platform APIGroup BEFORE
	// credentials projection. Without this guard a BackupClass that
	// resolves to e.g. strategy.backups.cozystack.io/MadeUpKind would
	// leak cozy-backups-creds into the tenant namespace and then
	// silently no-op in the dispatch switch below — leaving the
	// BackupJob in a phaseless state forever.
	supported := false
	for _, k := range supportedBackupStrategyKinds() {
		if strategyRef.Kind == k {
			supported = true
			break
		}
	}
	if !supported {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("strategy Kind %q is not supported by this controller (supported: %s)", strategyRef.Kind, strings.Join(supportedBackupStrategyKinds(), ", ")))
	}

	// Now project the platform-managed S3 credentials into the tenant
	// namespace so default Strategy CRs can reference a deterministic
	// Secret name. The projection is idempotent and silently skipped on
	// clusters where it is not configured (legacy chart-managed flow).
	//
	// Failure handling: SourceSecretMissing / APIError are transient — the
	// Bucket controller may not have produced the source Secret yet on a
	// fresh install. Mark Ready=False and requeue rather than terminally
	// failing the BackupJob (which would force tenants to recreate it and
	// would silently fail Plan-driven runs). TargetSecretNotOwned and
	// SourceSecretMalformed are operator-visible misconfigurations that
	// will not self-heal — fail terminally so the tenant gets a clear
	// message.
	if err := ProjectBackupCredentials(ctx, r.Client, r.CredentialsConfig, j.Namespace); err != nil {
		return r.handleProjectionError(ctx, j, err)
	}

	logger.Info("processing BackupJob", "backupjob", j.Name, "strategyKind", strategyRef.Kind, "backupClassName", j.Spec.BackupClassName)
	switch strategyRef.Kind {
	case strategyv1alpha1.JobStrategyKind:
		return r.reconcileJob(ctx, j, resolved)
	case strategyv1alpha1.VeleroStrategyKind:
		return r.reconcileVelero(ctx, j, resolved)
	case strategyv1alpha1.CNPGStrategyKind:
		return r.reconcileCNPG(ctx, j, resolved)
	case strategyv1alpha1.AltinityStrategyKind:
		return r.reconcileAltinity(ctx, j, resolved)
	case strategyv1alpha1.MariaDBStrategyKind:
		return r.reconcileMariaDB(ctx, j, resolved)
	case strategyv1alpha1.FoundationDBStrategyKind:
		return r.reconcileFoundationDB(ctx, j, resolved)
	case strategyv1alpha1.EtcdStrategyKind:
		return r.reconcileEtcd(ctx, j, resolved)
	default:
		logger.V(1).Info("BackupJob resolved StrategyRef.Kind not supported, skipping",
			"backupjob", j.Name,
			"kind", strategyRef.Kind,
			"supported", supportedBackupStrategyKinds())
		return ctrl.Result{}, nil
	}
}

// supportedBackupStrategyKinds returns every strategy.Kind the dispatch
// switch above handles. Centralised so the unsupported-strategy diagnostic
// can't drift out of sync with the real dispatch table - the unit test
// TestSupportedBackupStrategyKindsMatchesDispatch locks in this invariant.
func supportedBackupStrategyKinds() []string {
	return []string{
		strategyv1alpha1.JobStrategyKind,
		strategyv1alpha1.VeleroStrategyKind,
		strategyv1alpha1.CNPGStrategyKind,
		strategyv1alpha1.AltinityStrategyKind,
		strategyv1alpha1.MariaDBStrategyKind,
		strategyv1alpha1.FoundationDBStrategyKind,
		strategyv1alpha1.EtcdStrategyKind,
	}
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *BackupJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// index BackupJob by backupClassName for efficient lookups when BackupClass changes
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &backupsv1alpha1.BackupJob{}, "spec.backupClassName", func(obj client.Object) []string {
		job := obj.(*backupsv1alpha1.BackupJob)
		if job.Spec.BackupClassName == "" {
			return []string{}
		}
		return []string{job.Spec.BackupClassName}
	}); err != nil {
		return err
	}

	cfg := mgr.GetConfig()
	var err error
	if r.Interface, err = dynamic.NewForConfig(cfg); err != nil {
		return err
	}
	var h *http.Client
	if h, err = rest.HTTPClientFor(cfg); err != nil {
		return err
	}
	if r.RESTMapper, err = apiutil.NewDynamicRESTMapper(cfg, h); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.BackupJob{}).
		Complete(r)
}

// handleProjectionError classifies a credentials-projection error as
// transient (requeue) or terminal (mark Failed). Transient errors record
// a Ready=False condition without setting Phase=Failed so the BackupJob
// resumes once the underlying issue (source Secret propagation, apiserver
// hiccup) clears.
func (r *BackupJobReconciler) handleProjectionError(ctx context.Context, j *backupsv1alpha1.BackupJob, err error) (ctrl.Result, error) {
	logger := getLogger(ctx)
	// Tenant-side projection failures must be visible in the same
	// Prometheus counter as system-side failures so the
	// "rate(failures_total) > 0 or absent_over_time(successes_total[10m])"
	// alert in docs/operations/backup-classes.md catches them. The system
	// projector reports against system namespaces (cozy-velero etc.);
	// here we attribute against the BackupJob's tenant namespace.
	credentialsProjectionFailures.WithLabelValues(j.Namespace, classifyReason(err)).Inc()
	if IsTransient(err) {
		meta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "CredentialsProjectionPending",
			Message: err.Error(),
		})
		if updateErr := r.Status().Update(ctx, j); updateErr != nil {
			logger.Error(updateErr, "failed to update BackupJob status to projection-pending")
		}
		logger.Info("backup credentials projection transient failure; requeueing", "message", err.Error())
		return ctrl.Result{RequeueAfter: CredentialsProjectionRequeue}, nil
	}
	return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to project backup credentials: %v", err))
}

// markBackupJobFailed records a terminal Failed phase on the BackupJob.
//
// Coupling note: a failure that fires before reconcileCNPG's StartedAt block
// has set backupJob.Status.StartedAt leaves StartedAt nil. The CNPG path's
// cnpgDefaultBackupDeadline check only fires once StartedAt is set, so an
// early-failure retry that reaches the StartedAt block restarts the deadline
// budget from the retry's StartedAt - intentional, since retries are
// user-driven and a fresh budget is what users expect after correcting the
// cause of the previous failure.
func (r *BackupJobReconciler) markBackupJobFailed(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, message string) (ctrl.Result, error) {
	logger := getLogger(ctx)
	now := metav1.Now()
	backupJob.Status.CompletedAt = &now
	backupJob.Status.Phase = backupsv1alpha1.BackupJobPhaseFailed
	backupJob.Status.Message = message

	// SetStatusCondition keeps Conditions matching the +listType=map +listMapKey=type
	// CRD contract: a previously-set Ready condition (e.g. from a transient
	// retry path that flipped through ConditionFalse with a different
	// Reason) is updated in-place rather than appended, and LastTransitionTime
	// is preserved unless Status changes.
	meta.SetStatusCondition(&backupJob.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "BackupFailed",
		Message: message,
	})

	if err := r.Status().Update(ctx, backupJob); err != nil {
		logger.Error(err, "failed to update BackupJob status to Failed")
		return ctrl.Result{}, err
	}
	logger.Debug("BackupJob failed", "message", message)
	return ctrl.Result{}, nil
}

// StrategyNotReadyDeadline bounds how long a BackupJob/RestoreJob may sit in
// the transient StrategyNotReady state before it is failed terminally. It must
// be comfortably longer than a Flux HelmRelease reconcile cycle (default 10m)
// so a legitimate bootstrap self-heal — backupstrategy-controller ships the
// cozy-default BackupClass before its Strategy CRs, which are gated on the
// BucketClaim status — is never killed mid-flight. 30m gives ~3 Flux cycles,
// matching mariadbDefaultBackupDeadline.
const StrategyNotReadyDeadline = 30 * time.Minute

// strategyNotReadyDeadlineExceeded reports whether a job that has been waiting
// on a missing Strategy CR has exhausted the bootstrap-window grace period.
// Returns false when StartedAt is nil so the first reconcile (which sets it)
// never trips the gate.
func strategyNotReadyDeadlineExceeded(startedAt *metav1.Time) bool {
	if startedAt == nil {
		return false
	}
	return time.Since(startedAt.Time) > StrategyNotReadyDeadline
}

// requeueStrategyNotReady surfaces a transient Ready=False/StrategyNotReady
// condition on the BackupJob and requeues. Drivers call this when the
// strategyRef.name referenced by a (resolved) BackupClass points at a
// Strategy CR that does not exist yet — which happens during the
// platform bootstrap window where backupstrategy-controller's chart
// has rendered cozy-default BackupClass (always present) but its
// strategy templates are still gated on the BucketClaim status
// reconciling. Flux re-renders the chart once the BucketClaim status
// populates, the Strategy CR appears, and the next reconcile picks it
// up. Terminal failure here would force tenants to recreate BackupJobs
// every time they raced the bootstrap.
//
// The wait is bounded by StrategyNotReadyDeadline: a strategyRef.name that
// never resolves (e.g. a typo in a custom BackupClass) would otherwise requeue
// forever with no operator-visible "this will never succeed" signal. Mirrors
// the MariaDB CR-existence deadline. Every driver sets StartedAt before this is
// called, so the clock has already started.
func (r *BackupJobReconciler) requeueStrategyNotReady(ctx context.Context, j *backupsv1alpha1.BackupJob, strategyName string) (ctrl.Result, error) {
	logger := getLogger(ctx)
	if strategyNotReadyDeadlineExceeded(j.Status.StartedAt) {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf(
			"Strategy %q referenced by BackupClass %q was not provisioned within %s; check the strategyRef name or the platform backup-storage bootstrap",
			strategyName, j.Spec.BackupClassName, StrategyNotReadyDeadline))
	}
	meta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "StrategyNotReady",
		Message: fmt.Sprintf("Strategy %q referenced by BackupClass %q is not yet provisioned; the platform may still be initialising backup storage", strategyName, j.Spec.BackupClassName),
	})
	if updateErr := r.Status().Update(ctx, j); updateErr != nil {
		logger.Error(updateErr, "failed to update BackupJob status to StrategyNotReady")
	}
	return ctrl.Result{RequeueAfter: CredentialsProjectionRequeue}, nil
}
