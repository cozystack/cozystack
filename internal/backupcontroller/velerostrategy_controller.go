package backupcontroller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/template"

	"github.com/go-logr/logr"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

func getLogger(ctx context.Context) loggerWithDebug {
	return loggerWithDebug{Logger: log.FromContext(ctx)}
}

// loggerWithDebug wraps a logr.Logger and provides a Debug() method
// that maps to V(1).Info() for convenience.
type loggerWithDebug struct {
	logr.Logger
}

// Debug logs at debug level (equivalent to V(1).Info())
func (l loggerWithDebug) Debug(msg string, keysAndValues ...interface{}) {
	l.Logger.V(1).Info(msg, keysAndValues...)
}

// S3Credentials holds the discovered S3 credentials from a Bucket storageRef
type S3Credentials struct {
	BucketName      string
	Endpoint        string
	Region          string
	AccessKeyID     string
	AccessSecretKey string
}

const (
	defaultRequeueAfter                 = 5 * time.Second
	defaultActiveJobPollingInterval     = defaultRequeueAfter
	defaultRestoreRequeueAfter          = 5 * time.Second
	defaultActiveRestorePollingInterval = defaultRestoreRequeueAfter
	// Velero requires API objects and secrets to be in the cozy-velero namespace
	veleroNamespace                  = "cozy-velero"
	veleroBackupNameMetadataKey      = "velero.io/backup-name"
	veleroBackupNamespaceMetadataKey = "velero.io/backup-namespace"
)

func boolPtr(b bool) *bool {
	return &b
}

func (r *BackupJobReconciler) reconcileVelero(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Velero strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	// If already completed, no need to reconcile
	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		logger.Debug("BackupJob already completed, skipping", "phase", j.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Step 1: On first reconcile, set startedAt (but not phase yet - phase will be set after backup creation)
	logger.Debug("checking BackupJob status", "startedAt", j.Status.StartedAt, "phase", j.Status.Phase)
	if j.Status.StartedAt == nil {
		logger.Debug("setting BackupJob StartedAt")
		now := metav1.Now()
		j.Status.StartedAt = &now
		// Don't set phase to Running yet - will be set after Velero backup is successfully created
		if err := r.Status().Update(ctx, j); err != nil {
			logger.Error(err, "failed to update BackupJob status")
			return ctrl.Result{}, err
		}
		logger.Debug("set BackupJob StartedAt", "startedAt", j.Status.StartedAt)
	} else {
		logger.Debug("BackupJob already started", "startedAt", j.Status.StartedAt, "phase", j.Status.Phase)
	}

	// Step 2: Resolve inputs - Read Strategy from resolved config
	logger.Debug("fetching Velero strategy", "strategyName", resolved.StrategyRef.Name)
	veleroStrategy := &strategyv1alpha1.Velero{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, veleroStrategy); err != nil {
		if errors.IsNotFound(err) {
			logger.Error(err, "Velero strategy not found", "strategyName", resolved.StrategyRef.Name)
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Velero strategy not found: %s", resolved.StrategyRef.Name))
		}
		logger.Error(err, "failed to get Velero strategy")
		return ctrl.Result{}, err
	}
	logger.Debug("fetched Velero strategy", "strategyName", veleroStrategy.Name)

	// Step 3: Execute backup logic
	// Check if we already created a Velero Backup
	if j.Status.StartedAt == nil {
		logger.Error(nil, "StartedAt is nil after status update, this should not happen")
		return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
	}
	logger.Debug("checking for existing Velero Backup", "namespace", veleroNamespace)
	veleroBackupList := &velerov1.BackupList{}
	opts := []client.ListOption{
		client.InNamespace(veleroNamespace),
		client.MatchingLabels{
			backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
			backupsv1alpha1.OwningJobNameLabel:      j.Name,
		},
	}

	if err := r.List(ctx, veleroBackupList, opts...); err != nil {
		logger.Error(err, "failed to get Velero Backup")
		return ctrl.Result{}, err
	}

	if len(veleroBackupList.Items) == 0 {
		// Create Velero Backup
		logger.Debug("Velero Backup not found, creating new one")
		if err := r.createVeleroBackup(ctx, j, veleroStrategy, resolved); err != nil {
			logger.Error(err, "failed to create Velero Backup")
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to create Velero Backup: %v", err))
		}
		// After successful Velero backup creation, set phase to Running
		if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
			logger.Debug("setting BackupJob phase to Running after successful Velero backup creation")
			j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
			if err := r.Status().Update(ctx, j); err != nil {
				logger.Error(err, "failed to update BackupJob phase to Running")
				return ctrl.Result{}, err
			}
		}
		logger.Debug("created Velero Backup, requeuing")
		// Requeue to check status
		return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
	}

	if len(veleroBackupList.Items) > 1 {
		logger.Error(fmt.Errorf("too many Velero backups for BackupJob"), "found more than one Velero Backup referencing a single BackupJob as owner")
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseFailed
		if err := r.Status().Update(ctx, j); err != nil {
			logger.Error(err, "failed to update BackupJob status")
		}
		return ctrl.Result{}, nil
	}

	veleroBackup := veleroBackupList.Items[0].DeepCopy()
	logger.Debug("found existing Velero Backup", "phase", veleroBackup.Status.Phase)

	// If Velero backup exists but phase is not Running, set it to Running
	// This handles the case where the backup was created but phase wasn't set yet
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		logger.Debug("setting BackupJob phase to Running (Velero backup already exists)")
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
		if err := r.Status().Update(ctx, j); err != nil {
			logger.Error(err, "failed to update BackupJob phase to Running")
			return ctrl.Result{}, err
		}
	}

	// Check Velero Backup status
	phase := string(veleroBackup.Status.Phase)
	if phase == "" {
		// Still in progress, requeue
		return ctrl.Result{RequeueAfter: defaultActiveJobPollingInterval}, nil
	}

	// Step 4: On success - Create Backup resource and update status
	if phase == "Completed" {
		// Check if we already created the Backup resource
		if j.Status.BackupRef == nil {
			backup, err := r.createBackupResource(ctx, j, veleroBackup, resolved)
			if err != nil {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to create Backup resource: %v", err))
			}

			now := metav1.Now()
			j.Status.BackupRef = &corev1.LocalObjectReference{Name: backup.Name}
			j.Status.CompletedAt = &now
			j.Status.Phase = backupsv1alpha1.BackupJobPhaseSucceeded
			if err := r.Status().Update(ctx, j); err != nil {
				logger.Error(err, "failed to update BackupJob status")
				return ctrl.Result{}, err
			}
			logger.Debug("BackupJob succeeded", "backup", backup.Name)
		}
		return ctrl.Result{}, nil
	}

	// Step 5: On failure
	if phase == "Failed" || phase == "PartiallyFailed" {
		message := fmt.Sprintf("Velero Backup failed with phase: %s", phase)
		if len(veleroBackup.Status.ValidationErrors) > 0 {
			message = fmt.Sprintf("%s: %v", message, veleroBackup.Status.ValidationErrors)
		}
		return r.markBackupJobFailed(ctx, j, message)
	}

	// Still in progress (InProgress, New, etc.)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *BackupJobReconciler) createVeleroBackup(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, strategy *strategyv1alpha1.Velero, resolved *ResolvedBackupConfig) error {
	logger := getLogger(ctx)
	logger.Debug("createVeleroBackup called", "strategy", strategy.Name)

	mapping, err := r.RESTMapping(schema.GroupKind{Group: *backupJob.Spec.ApplicationRef.APIGroup, Kind: backupJob.Spec.ApplicationRef.Kind})
	if err != nil {
		return err
	}
	ns := backupJob.Namespace
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		ns = ""
	}
	app, err := r.Resource(mapping.Resource).Namespace(ns).Get(ctx, backupJob.Spec.ApplicationRef.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	templateContext := map[string]interface{}{
		"Application": app.Object,
		"Parameters":  resolved.Parameters,
	}

	veleroBackupSpec, err := template.Template(&strategy.Spec.Template.Spec, templateContext)
	if err != nil {
		return err
	}
	veleroBackup := &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s.%s-", backupJob.Namespace, backupJob.Name),
			Namespace:    veleroNamespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      backupJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: backupJob.Namespace,
			},
		},
		Spec: *veleroBackupSpec,
	}
	name := veleroBackup.GenerateName
	if err := r.Create(ctx, veleroBackup); err != nil {
		if veleroBackup.Name != "" {
			name = veleroBackup.Name
		}
		logger.Error(err, "failed to create Velero Backup", "name", veleroBackup.Name)
		r.Recorder.Event(backupJob, corev1.EventTypeWarning, "VeleroBackupCreationFailed",
			fmt.Sprintf("Failed to create Velero Backup %s/%s: %v", veleroNamespace, name, err))
		return err
	}

	logger.Debug("created Velero Backup", "name", veleroBackup.Name, "namespace", veleroBackup.Namespace)
	r.Recorder.Event(backupJob, corev1.EventTypeNormal, "VeleroBackupCreated",
		fmt.Sprintf("Created Velero Backup %s/%s", veleroNamespace, name))
	return nil
}

func (r *BackupJobReconciler) createBackupResource(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, veleroBackup *velerov1.Backup, resolved *ResolvedBackupConfig) (*backupsv1alpha1.Backup, error) {
	logger := getLogger(ctx)

	// Get takenAt from Velero Backup creation timestamp or status
	takenAt := metav1.Now()
	if veleroBackup.Status.StartTimestamp != nil {
		takenAt = *veleroBackup.Status.StartTimestamp
	} else if !veleroBackup.CreationTimestamp.IsZero() {
		takenAt = veleroBackup.CreationTimestamp
	}

	// Extract driver metadata (e.g., Velero backup name)
	driverMetadata := map[string]string{
		veleroBackupNameMetadataKey:      veleroBackup.Name,
		veleroBackupNamespaceMetadataKey: veleroBackup.Namespace,
	}

	// Create a basic artifact referencing the Velero backup
	artifact := &backupsv1alpha1.BackupArtifact{
		URI: fmt.Sprintf("velero://%s/%s", veleroBackup.Namespace, veleroBackup.Name),
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupJob.Name,
			Namespace: backupJob.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: backupJob.APIVersion,
					Kind:       backupJob.Kind,
					Name:       backupJob.Name,
					UID:        backupJob.UID,
					Controller: boolPtr(true),
				},
			},
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: backupJob.Spec.ApplicationRef,
			StrategyRef:    resolved.StrategyRef,
			TakenAt:        takenAt,
			DriverMetadata: driverMetadata,
		},
		Status: backupsv1alpha1.BackupStatus{
			Phase:    backupsv1alpha1.BackupPhaseReady,
			Artifact: artifact,
		},
	}

	if backupJob.Spec.PlanRef != nil {
		backup.Spec.PlanRef = backupJob.Spec.PlanRef
	}

	if err := r.Create(ctx, backup); err != nil {
		logger.Error(err, "failed to create Backup resource")
		return nil, err
	}

	logger.Debug("created Backup resource", "name", backup.Name)
	return backup, nil
}

// reconcileVeleroRestore handles restore operations for Velero strategy.
func (r *RestoreJobReconciler) reconcileVeleroRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Velero strategy restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	// Step 1: On first reconcile, set startedAt and phase = Running
	if restoreJob.Status.StartedAt == nil {
		logger.Debug("setting RestoreJob StartedAt and phase to Running")
		now := metav1.Now()
		restoreJob.Status.StartedAt = &now
		restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			logger.Error(err, "failed to update RestoreJob status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
	}

	// Step 2: Resolve inputs - Read Strategy, Storage, target Application
	logger.Debug("fetching Velero strategy", "strategyName", backup.Spec.StrategyRef.Name)
	veleroStrategy := &strategyv1alpha1.Velero{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, veleroStrategy); err != nil {
		if errors.IsNotFound(err) {
			logger.Error(err, "Velero strategy not found", "strategyName", backup.Spec.StrategyRef.Name)
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("Velero strategy not found: %s", backup.Spec.StrategyRef.Name))
		}
		logger.Error(err, "failed to get Velero strategy")
		return ctrl.Result{}, err
	}
	logger.Debug("fetched Velero strategy", "strategyName", veleroStrategy.Name)

	// Get Velero backup name from Backup's driverMetadata
	veleroBackupName, ok := backup.Spec.DriverMetadata[veleroBackupNameMetadataKey]
	if !ok {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("Backup missing Velero backup name in driverMetadata (key: %s)", veleroBackupNameMetadataKey))
	}

	// Step 3: Execute restore logic
	// Check if we already created a Velero Restore
	logger.Debug("checking for existing Velero Restore", "namespace", veleroNamespace)
	veleroRestoreList := &velerov1.RestoreList{}
	opts := []client.ListOption{
		client.InNamespace(veleroNamespace),
		client.MatchingLabels{
			backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
		},
	}

	if err := r.List(ctx, veleroRestoreList, opts...); err != nil {
		logger.Error(err, "failed to get Velero Restore")
		return ctrl.Result{}, err
	}

	if len(veleroRestoreList.Items) == 0 {
		// Create Velero Restore
		logger.Debug("Velero Restore not found, creating new one")
		if err := r.createVeleroRestore(ctx, restoreJob, backup, veleroStrategy, veleroBackupName); err != nil {
			logger.Error(err, "failed to create Velero Restore")
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("failed to create Velero Restore: %v", err))
		}
		logger.Debug("created Velero Restore, requeuing")
		// Requeue to check status
		return ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
	}

	if len(veleroRestoreList.Items) > 1 {
		logger.Error(fmt.Errorf("too many Velero restores for RestoreJob"), "found more than one Velero Restore referencing a single RestoreJob as owner")
		return r.markRestoreJobFailed(ctx, restoreJob, "found multiple Velero Restores for this RestoreJob")
	}

	veleroRestore := veleroRestoreList.Items[0].DeepCopy()
	logger.Debug("found existing Velero Restore", "phase", veleroRestore.Status.Phase)

	// Check Velero Restore status
	phase := string(veleroRestore.Status.Phase)
	if phase == "" {
		// Still in progress, requeue
		return ctrl.Result{RequeueAfter: defaultActiveRestorePollingInterval}, nil
	}

	// Step 4: On success
	if phase == "Completed" {
		now := metav1.Now()
		restoreJob.Status.CompletedAt = &now
		restoreJob.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
		if err := r.Status().Update(ctx, restoreJob); err != nil {
			logger.Error(err, "failed to update RestoreJob status")
			return ctrl.Result{}, err
		}
		logger.Debug("RestoreJob succeeded")
		return ctrl.Result{}, nil
	}

	// Step 5: On failure
	if phase == "Failed" || phase == "PartiallyFailed" {
		message := fmt.Sprintf("Velero Restore failed with phase: %s", phase)
		if veleroRestore.Status.FailureReason != "" {
			message = fmt.Sprintf("%s: %s", message, veleroRestore.Status.FailureReason)
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)
	}

	// Still in progress (InProgress, New, etc.)
	return ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
}

// createVeleroRestore creates a Velero Restore resource.
func (r *RestoreJobReconciler) createVeleroRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, strategy *strategyv1alpha1.Velero, veleroBackupName string) error {
	logger := getLogger(ctx)
	logger.Debug("createVeleroRestore called", "strategy", strategy.Name, "veleroBackupName", veleroBackupName)

	// Determine target application reference
	targetAppRef := r.getTargetApplicationRef(restoreJob, backup)

	// Get the target application object for templating
	mapping, err := r.RESTMapping(schema.GroupKind{Group: *targetAppRef.APIGroup, Kind: targetAppRef.Kind})
	if err != nil {
		return fmt.Errorf("failed to get REST mapping for target application: %w", err)
	}
	ns := restoreJob.Namespace
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		ns = ""
	}
	app, err := r.Resource(mapping.Resource).Namespace(ns).Get(ctx, targetAppRef.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get target application: %w", err)
	}

	// Build template context
	templateContext := map[string]interface{}{
		"Application": app.Object,
		// TODO: Parameters are not currently stored on Backup, so they're unavailable during restore.
		// This is a design limitation that should be addressed by persisting Parameters on the Backup object.
		"Parameters": map[string]string{},
	}

	// Template the restore spec from the strategy, or use defaults if not specified
	var veleroRestoreSpec velerov1.RestoreSpec
	if strategy.Spec.Template.RestoreSpec != nil {
		templatedSpec, err := template.Template(strategy.Spec.Template.RestoreSpec, templateContext)
		if err != nil {
			return fmt.Errorf("failed to template Velero Restore spec: %w", err)
		}
		veleroRestoreSpec = *templatedSpec
	}

	// Set the backupName in the spec (required by Velero)
	veleroRestoreSpec.BackupName = veleroBackupName

	generateName := fmt.Sprintf("%s.%s-", restoreJob.Namespace, restoreJob.Name)
	veleroRestore := &velerov1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
			Namespace:    veleroNamespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
			},
		},
		Spec: veleroRestoreSpec,
	}
	if err := r.Create(ctx, veleroRestore); err != nil {
		logger.Error(err, "failed to create Velero Restore", "generateName", generateName)
		r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "VeleroRestoreCreationFailed",
			fmt.Sprintf("Failed to create Velero Restore %s/%s: %v", veleroNamespace, generateName, err))
		return err
	}

	logger.Debug("created Velero Restore", "name", veleroRestore.Name, "namespace", veleroRestore.Namespace)
	r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "VeleroRestoreCreated",
		fmt.Sprintf("Created Velero Restore %s/%s", veleroNamespace, veleroRestore.Name))
	return nil
}
