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

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

const backupFinalizer = "backups.cozystack.io/cleanup-velero"

// BackupReconciler reconciles Backup objects.
// It manages a finalizer that ensures the underlying Velero backup is deleted
// when the cozystack Backup resource is deleted.
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

	// Handle deletion: clean up Velero backup
	if !backup.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(backup, backupFinalizer) {
			if err := r.cleanupVeleroBackup(ctx, backup); err != nil {
				logger.Error(err, "failed to clean up Velero backup")
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(backup, backupFinalizer)
			if err := r.Update(ctx, backup); err != nil {
				return ctrl.Result{}, err
			}
			logger.V(1).Info("removed finalizer and cleaned up Velero backup", "backup", backup.Name)
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

// SetupWithManager registers the BackupReconciler with the Manager.
func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.Backup{}).
		Complete(r)
}
