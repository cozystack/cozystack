package backupcontroller

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func (r *BackupJobReconciler) reconcileVelero(ctx context.Context, j *backupsv1alpha1.BackupJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	backupJob := j
	if err := r.Get(ctx, req.NamespacedName, backupJob); err != nil {
		return ctrl.Result{}, err
	}

	// Check if this BackupJob matches our strategy type (Velero)
	if backupJob.Spec.StrategyRef.Kind != "Velero"  {
		// Not a Velero strategy, ignore
		return ctrl.Result{}, nil
	}

	// If already completed, no need to reconcile
	if backupJob.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		backupJob.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	// For now implemented backup logic for App Kubevirt VirtualMachine only
	if backupJob.Spec.ApplicationRef.Kind != "VirtualMachine" ||
		backupJob.Spec.ApplicationRef.APIGroup != "kubevirt.io/v1" {
		return r.markBackupJobFailed(ctx, backupJob, fmt.Sprintf("Unsupported application type: %s", backupJob.Spec.ApplicationRef.Kind), logger)
	}

	// Step 1: On first reconcile, set startedAt and phase = Running
	if backupJob.Status.StartedAt == nil {
		now := metav1.Now()
		backupJob.Status.StartedAt = &now
		backupJob.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
		if err := r.Status().Update(ctx, backupJob); err != nil {
			logger.Error(err, "failed to update BackupJob status")
			return ctrl.Result{}, err
		}
		logger.Info("started BackupJob", "phase", backupJob.Status.Phase)
	}

	// Step 2: Resolve inputs - Read Strategy, Storage, Application, optionally Plan
	veleroStrategy := &velerostrategyv1alpha1.Velero{}
	if err := r.Get(ctx, client.ObjectKey{Name: backupJob.Spec.StrategyRef.Name}, veleroStrategy); err != nil {
		if errors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, backupJob, fmt.Sprintf("Velero strategy not found: %s", backupJob.Spec.StrategyRef.Name), logger)
		}
		return ctrl.Result{}, err
	}

	// Step 3: Execute backup logic
	// Check if we already created a Velero Backup
	// Use human-readable timestamp: YYYY-MM-DD-HH-MM-SS
	timestamp := backupJob.Status.StartedAt.Time.Format("2006-01-02-15-04-05")
	veleroBackupName := fmt.Sprintf("%s-velero-%s", backupJob.Name, timestamp)
	veleroBackup := &unstructured.Unstructured{}
	veleroBackup.SetAPIVersion("velero.io/v1")
	veleroBackup.SetKind("Backup")
	veleroBackupKey := client.ObjectKey{Namespace: backupJob.Namespace, Name: veleroBackupName}

	if err := r.Get(ctx, veleroBackupKey, veleroBackup); err != nil {
		if errors.IsNotFound(err) {
			// Create Velero Backup
			if err := r.createVeleroBackup(ctx, backupJob, veleroBackupName, logger); err != nil {
				return r.markBackupJobFailed(ctx, backupJob, fmt.Sprintf("failed to create Velero Backup: %v", err), logger)
			}
			// Requeue to check status
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	// Check Velero Backup status
	phase, found, err := unstructured.NestedString(veleroBackup.Object, "status", "phase")
	if err != nil {
		return ctrl.Result{}, err
	}

	if !found || phase == "" {
		// Still in progress, requeue
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Step 4: On success - Create Backup resource and update status
	if phase == "Completed" {
		// Check if we already created the Backup resource
		if backupJob.Status.BackupRef == nil {
			backup, err := r.createBackupResource(ctx, backupJob, veleroBackup, logger)
			if err != nil {
				return r.markBackupJobFailed(ctx, backupJob, fmt.Sprintf("failed to create Backup resource: %v", err), logger)
			}

			now := metav1.Now()
			backupJob.Status.BackupRef = &corev1.LocalObjectReference{Name: backup.Name}
			backupJob.Status.CompletedAt = &now
			backupJob.Status.Phase = backupsv1alpha1.BackupJobPhaseSucceeded
			if err := r.Status().Update(ctx, backupJob); err != nil {
				logger.Error(err, "failed to update BackupJob status")
				return ctrl.Result{}, err
			}
			logger.Info("BackupJob succeeded", "backup", backup.Name)
		}
		return ctrl.Result{}, nil
	}

	// Step 5: On failure
	if phase == "Failed" || phase == "PartiallyFailed" {
		message, _, _ := unstructured.NestedString(veleroBackup.Object, "status", "message")
		if message == "" {
			message = fmt.Sprintf("Velero Backup failed with phase: %s", phase)
		}
		return r.markBackupJobFailed(ctx, backupJob, message, logger)
	}

	// Still in progress (InProgress, New, etc.)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *BackupVeleroStrategyReconciler) markBackupJobFailed(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, message string, logger log.Logger) (ctrl.Result, error) {
	now := metav1.Now()
	backupJob.Status.CompletedAt = &now
	backupJob.Status.Phase = backupsv1alpha1.BackupJobPhaseFailed
	backupJob.Status.Message = message

	// Add condition
	backupJob.Status.Conditions = append(backupJob.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "BackupFailed",
		Message:            message,
		LastTransitionTime: now,
	})

	if err := r.Status().Update(ctx, backupJob); err != nil {
		logger.Error(err, "failed to update BackupJob status to Failed")
		return ctrl.Result{}, err
	}
	logger.Info("BackupJob failed", "message", message)
	return ctrl.Result{}, nil
}

func (r *BackupVeleroStrategyReconciler) createVeleroBackup(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, name string, logger log.Logger) error {

	// Resolve the application to determine which VM to backup
	// If ApplicationRef points to a HelmRelease, we use label selector to find the VM it manages
	var labelSelector map[string]interface{}

	if backupJob.Spec.ApplicationRef.Kind == "HelmRelease" {
		// If it's a HelmRelease, use label selector to match the VM it manages
		// VMs managed by HelmRelease have label: helm.toolkit.fluxcd.io/name=<helmrelease-name>
		labelSelector = map[string]interface{}{
			"matchLabels": map[string]interface{}{
				"helm.toolkit.fluxcd.io/name": backupJob.Spec.ApplicationRef.Name,
			},
		}
	} else if backupJob.Spec.ApplicationRef.Kind == "VirtualMachine" {
		// If it directly references a VM, we could use a label selector if the VM has a unique label
		// For now, we'll backup all VMs in the namespace (the includedResources filter will limit to VMs only)
		// TODO: Add label selector for direct VM references if needed
	}

	// Create a Velero Backup (velero.io/v1) using unstructured object
	// Only backup VirtualMachine resources
	spec := map[string]interface{}{
		"includedNamespaces": []string{backupJob.Namespace},
		"includedResources":   []string{"virtualmachines.kubevirt.io"},
		// TODO: Resolve StorageRef to get the BackupStorageLocation name
		// For now, using "default" as a placeholder
		// This should be resolved from the Storage CRD referenced in backupJob.Spec.StorageRef
		"storageLocation": "default",
	}

	// Add label selector if we have one (for HelmRelease-managed VMs)
	if labelSelector != nil {
		spec["labelSelector"] = labelSelector
	}

	veleroBackup := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "velero.io/v1",
			"kind":       "Backup",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": backupJob.Namespace,
			},
			"spec": spec,
		},
	}

	// Set owner reference to the BackupJob
	veleroBackup.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: backupJob.APIVersion,
			Kind:       backupJob.Kind,
			Name:       backupJob.Name,
			UID:        backupJob.UID,
		},
	})

	if err := r.Create(ctx, veleroBackup); err != nil {
		logger.Error(err, "failed to create Velero Backup", "name", veleroBackup.GetName())
		return err
	}

	logger.Info("created Velero Backup", "name", veleroBackup.GetName(), "namespace", veleroBackup.GetNamespace())
	return nil
}

func (r *BackupVeleroStrategyReconciler) createBackupResource(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, veleroBackup *unstructured.Unstructured, logger log.Logger) (*backupsv1alpha1.Backup, error) {
	// Extract artifact information from Velero Backup
	var artifact *backupsv1alpha1.BackupArtifact
	artifactMap, found, err := unstructured.NestedMap(veleroBackup.Object, "status", "backupItemOperations")
	if err == nil && found && len(artifactMap) > 0 {
		// Try to get backup location and snapshot info
		// For now, we'll create a basic artifact
		artifact = &backupsv1alpha1.BackupArtifact{
			URI: fmt.Sprintf("velero://%s/%s", backupJob.Namespace, veleroBackup.GetName()),
		}
	}

	// Get takenAt from Velero Backup creation timestamp or status
	takenAt := metav1.Now()
	if startTime, found, err := unstructured.NestedString(veleroBackup.Object, "status", "startTimestamp"); err == nil && found {
		if t, err := time.Parse(time.RFC3339, startTime); err == nil {
			takenAt = metav1.NewTime(t)
		}
	} else if creationTime := veleroBackup.GetCreationTimestamp(); !creationTime.IsZero() {
		takenAt = creationTime
	}

	// Extract driver metadata (e.g., Velero backup name)
	driverMetadata := map[string]string{
		"velero.io/backup-name":      veleroBackup.GetName(),
		"velero.io/backup-namespace":  veleroBackup.GetNamespace(),
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-backup", backupJob.Name),
			Namespace: backupJob.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: backupJob.APIVersion,
					Kind:       backupJob.Kind,
					Name:       backupJob.Name,
					UID:        backupJob.UID,
				},
			},
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: backupJob.Spec.ApplicationRef,
			StorageRef:     backupJob.Spec.StorageRef,
			StrategyRef:    backupJob.Spec.StrategyRef,
			TakenAt:        takenAt,
			DriverMetadata: driverMetadata,
		},
		Status: backupsv1alpha1.BackupStatus{
			Phase: backupsv1alpha1.BackupPhaseReady,
		},
	}

	if backupJob.Spec.PlanRef != nil {
		backup.Spec.PlanRef = backupJob.Spec.PlanRef
	}

	if artifact != nil {
		backup.Status.Artifact = artifact
	}

	if err := r.Create(ctx, backup); err != nil {
		logger.Error(err, "failed to create Backup resource")
		return nil, err
	}

	logger.Info("created Backup resource", "name", backup.Name)
	return backup, nil
}
