package backupcontroller

import (
	"context"
	"net/http"

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
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
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

	// Normalize ApplicationRef (default apiGroup if not specified)
	normalizedAppRef := NormalizeApplicationRef(j.Spec.ApplicationRef)

	// Resolve BackupClass
	resolved, err := ResolveBackupClass(ctx, r.Client, j.Spec.BackupClassName, normalizedAppRef)
	if err != nil {
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
