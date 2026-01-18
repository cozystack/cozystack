package backupcontroller

import (
	"context"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

// BackupVeleroStrategyReconciler reconciles BackupJob with a strategy referencing
// Velero.strategy.backups.cozystack.io objects.
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
	normalizedAppRef := normalizeApplicationRef(j.Spec.ApplicationRef)

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
	default:
		logger.V(1).Info("BackupJob resolved StrategyRef.Kind not supported, skipping",
			"backupjob", j.Name,
			"kind", strategyRef.Kind,
			"supported", []string{strategyv1alpha1.JobStrategyKind, strategyv1alpha1.VeleroStrategyKind})
		return ctrl.Result{}, nil
	}
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *BackupJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
