package backupcontroller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

// backupFinalizer guards the strategy-specific side state owned by a Backup.
// Velero artifacts (the velero.io/Backup CR plus its data on the BSL) are the
// historical reason for the finalizer, but CNPG-strategy Backups also own
// side state (a postgresql.cnpg.io/Backup CR) that must be cleaned up here.
const backupFinalizer = "backups.cozystack.io/cleanup"

// legacyVeleroBackupFinalizer is the finalizer name used by previous releases
// before the cleanup path was generalized across strategies. We still
// recognise it on existing objects so upgrades complete without leaking
// finalizers, but new Backups are stamped with backupFinalizer instead.
const legacyVeleroBackupFinalizer = "backups.cozystack.io/cleanup-velero"

// BackupReconciler reconciles Backup objects.
// It manages a finalizer that ensures strategy-owned side state (Velero or
// CNPG) is deleted when the cozystack Backup resource is deleted.
type BackupReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("reconciling Backup", "namespace", req.Namespace, "name", req.Name)

	backup := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, backup); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion: clean up strategy-specific side state.
	if !backup.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(backup, backupFinalizer) ||
			controllerutil.ContainsFinalizer(backup, legacyVeleroBackupFinalizer) {
			if err := r.cleanupOnDelete(ctx, backup); err != nil {
				logger.Error(err, "failed to clean up strategy-owned side state")
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(backup, backupFinalizer)
			controllerutil.RemoveFinalizer(backup, legacyVeleroBackupFinalizer)
			if err := r.Update(ctx, backup); err != nil {
				return ctrl.Result{}, err
			}
			logger.V(1).Info("removed finalizer and cleaned up side state", "backup", backup.Name)
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(backup, backupFinalizer) {
		controllerutil.AddFinalizer(backup, backupFinalizer)
		if err := r.Update(ctx, backup); err != nil {
			return ctrl.Result{}, err
		}
		logger.V(1).Info("added finalizer to Backup", "backup", backup.Name)
	}

	return ctrl.Result{}, nil
}

// cleanupOnDelete dispatches deletion cleanup to the driver that produced
// the Backup's side state. Mirrors restorejob_controller's cleanupOnDelete
// pattern - each strategy knows what it created and how to undo it. Velero
// owns the velero.io/Backup CR plus archive data on the BSL; CNPG owns the
// postgresql.cnpg.io/Backup CR; Job owns nothing namespace-scoped.
func (r *BackupReconciler) cleanupOnDelete(ctx context.Context, backup *backupsv1alpha1.Backup) error {
	logger := log.FromContext(ctx)
	kind := strategyKindForBackup(backup)
	logger.V(1).Info("dispatching Backup cleanup", "backup", backup.Name, "strategy", kind)
	switch kind {
	case strategyv1alpha1.CNPGStrategyKind:
		return r.cleanupCNPGBackup(ctx, backup)
	case strategyv1alpha1.JobStrategyKind:
		// Nothing to clean up: Job-strategy Backups own no namespace-scoped
		// artifacts that survive Backup deletion.
		return nil
	case strategyv1alpha1.VeleroStrategyKind:
		return r.cleanupVeleroBackup(ctx, backup)
	default:
		// Unknown or empty strategy. Conservative path: try the Velero
		// cleanup since it is keyed on a metadata field that only Velero
		// sets, so it is a no-op for non-Velero Backups. This preserves
		// backward compatibility with pre-existing objects.
		return r.cleanupVeleroBackup(ctx, backup)
	}
}

// strategyKindForBackup extracts the strategy kind from a Backup's
// strategyRef. Returns "" when the field is empty so callers can treat that
// as "unknown" and fall back to a safe default.
func strategyKindForBackup(backup *backupsv1alpha1.Backup) string {
	return backup.Spec.StrategyRef.Kind
}

// cleanupVeleroBackup deletes the Velero backup and its data from storage
// by creating a Velero DeleteBackupRequest. A direct Delete of the
// backup.velero.io resource only removes the Kubernetes object; Velero's
// BSL sync will recreate it from the object store. The DeleteBackupRequest
// tells Velero to also purge the data, preventing resurrection.
func (r *BackupReconciler) cleanupVeleroBackup(ctx context.Context, backup *backupsv1alpha1.Backup) error {
	logger := log.FromContext(ctx)

	veleroBackupName, ok := backup.Spec.DriverMetadata[veleroBackupNameMetadataKey]
	if !ok || veleroBackupName == "" {
		logger.V(1).Info("no Velero backup name in driverMetadata, nothing to clean up")
		return nil
	}

	veleroBackupNamespace := backup.Spec.DriverMetadata[veleroBackupNamespaceMetadataKey]
	if veleroBackupNamespace == "" {
		veleroBackupNamespace = veleroNamespace
	}

	// Check if the Velero Backup still exists
	veleroBackup := &velerov1.Backup{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: veleroBackupNamespace,
		Name:      veleroBackupName,
	}, veleroBackup)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Velero backup already deleted", "name", veleroBackupName)
			return nil
		}
		return fmt.Errorf("failed to get Velero backup %s/%s: %w", veleroBackupNamespace, veleroBackupName, err)
	}

	// Create a DeleteBackupRequest so Velero removes backup data from storage.
	// Without this, BSL sync will recreate the backup.velero.io resource.
	dbr := &velerov1.DeleteBackupRequest{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: veleroBackupName + "-",
			Namespace:    veleroBackupNamespace,
		},
		Spec: velerov1.DeleteBackupRequestSpec{
			BackupName: veleroBackupName,
		},
	}
	if err := r.Create(ctx, dbr); err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.V(1).Info("DeleteBackupRequest already exists", "backup", veleroBackupName)
			return nil
		}
		return fmt.Errorf("failed to create DeleteBackupRequest for %s/%s: %w", veleroBackupNamespace, veleroBackupName, err)
	}

	logger.Info("created DeleteBackupRequest for Velero backup",
		"name", veleroBackupName, "namespace", veleroBackupNamespace,
		"deleteRequest", dbr.Name)
	return nil
}

// cleanupCNPGBackup deletes the postgresql.cnpg.io/Backup CR that the CNPG
// driver created for this Cozystack Backup. It does NOT purge the Barman
// archive in S3 - CNPG itself does not expose a "delete this archive"
// surface, and removing only the CR matches the operator's expectation that
// archive lifecycle is governed by spec.backup.retentionPolicy on the
// Cluster. Tenants who need point-in-time deletion must do it through the
// object store directly.
func (r *BackupReconciler) cleanupCNPGBackup(ctx context.Context, backup *backupsv1alpha1.Backup) error {
	logger := log.FromContext(ctx)

	cnpgBackupName, ok := backup.Spec.DriverMetadata[cnpgBackupNameKey]
	if !ok || cnpgBackupName == "" {
		logger.V(1).Info("no cnpg.io backup name in driverMetadata, nothing to clean up")
		return nil
	}

	cnpgBackup := &cnpgtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: backup.Namespace,
			Name:      cnpgBackupName,
		},
	}
	if err := r.Delete(ctx, cnpgBackup); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete cnpg.io/Backup %s/%s: %w", backup.Namespace, cnpgBackupName, err)
	}
	logger.Info("deleted cnpg.io/Backup (or already absent)",
		"name", cnpgBackupName, "namespace", backup.Namespace)
	return nil
}

// SetupWithManager registers the BackupReconciler with the Manager.
func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.Backup{}).
		Complete(r)
}
