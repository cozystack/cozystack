package backupcontroller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	velerostrategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
)

// S3Credentials holds the discovered S3 credentials from a Bucket storageRef
type S3Credentials struct {
	BucketName      string
	Endpoint        string
	Region          string
	AccessKeyID     string
	AccessSecretKey string
}

// bucketInfo represents the structure of BucketInfo stored in the secret
type bucketInfo struct {
	Spec struct {
		BucketName string `json:"bucketName"`
		SecretS3   struct {
			Endpoint        string `json:"endpoint"`
			Region          string `json:"region"`
			AccessKeyID     string `json:"accessKeyID"`
			AccessSecretKey string `json:"accessSecretKey"`
		} `json:"secretS3"`
	} `json:"spec"`
}

func (r *BackupJobReconciler) reconcileVelero(ctx context.Context, j *backupsv1alpha1.BackupJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if this BackupJob matches our strategy type (Velero)
	if j.Spec.StrategyRef.Kind != "Velero"  {
		// Not a Velero strategy, ignore
		return ctrl.Result{}, nil
	}

	// If already completed, no need to reconcile
	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}

	// For now implemented backup logic for apps.cozystack.io VirtualMachine only
	if j.Spec.ApplicationRef.Kind != "VirtualMachine" ||
		j.Spec.ApplicationRef.APIGroup != "apps.cozystack.io/v1alpha1" {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Unsupported application type: %s", j.Spec.ApplicationRef.Kind), logger)
	}

	if j.Spec.StorageRef.Kind != "Bucket" ||
		j.Spec.StorageRef.APIGroup != "apps.cozystack.io/v1alpha1" {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Unsupported storage type: %s %s", j.Spec.StorageRef.Kind, j.Spec.StorageRef.APIGroup), logger)
	}

	// Step 1: On first reconcile, set startedAt and phase = Running
	if j.Status.StartedAt == nil {
		now := metav1.Now()
		j.Status.StartedAt = &now
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
		if err := r.Status().Update(ctx, j); err != nil {
			logger.Error(err, "failed to update BackupJob status")
			return ctrl.Result{}, err
		}
		logger.Info("started BackupJob", "phase", j.Status.Phase)
	}

	// Step 2: Resolve inputs - Read Strategy, Storage, Application, optionally Plan
	veleroStrategy := &velerostrategyv1alpha1.Velero{}
	if err := r.Get(ctx, client.ObjectKey{Name: j.Spec.StrategyRef.Name}, veleroStrategy); err != nil {
		if errors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Velero strategy not found: %s", j.Spec.StrategyRef.Name), logger)
		}
		return ctrl.Result{}, err
	}

	// Step 3: Execute backup logic
	// Check if we already created a Velero Backup
	// Use human-readable timestamp: YYYY-MM-DD-HH-MM-SS
	timestamp := j.Status.StartedAt.Time.Format("2006-01-02-15-04-05")
	veleroBackupName := fmt.Sprintf("%s-velero-%s", j.Name, timestamp)
	veleroBackup := &unstructured.Unstructured{}
	veleroBackup.SetAPIVersion("velero.io/v1")
	veleroBackup.SetKind("Backup")
	veleroBackupKey := client.ObjectKey{Namespace: j.Namespace, Name: veleroBackupName}

	if err := r.Get(ctx, veleroBackupKey, veleroBackup); err != nil {
		if errors.IsNotFound(err) {
			// Create Velero Backup
			if err := r.createVeleroBackup(ctx, j, veleroBackupName, logger); err != nil {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to create Velero Backup: %v", err), logger)
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
		if j.Status.BackupRef == nil {
			backup, err := r.createBackupResource(ctx, j, veleroBackup, logger)
			if err != nil {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to create Backup resource: %v", err), logger)
			}

			now := metav1.Now()
			j.Status.BackupRef = &corev1.LocalObjectReference{Name: backup.Name}
			j.Status.CompletedAt = &now
			j.Status.Phase = backupsv1alpha1.BackupJobPhaseSucceeded
			if err := r.Status().Update(ctx, j); err != nil {
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
		return r.markBackupJobFailed(ctx, j, message, logger)
	}

	// Still in progress (InProgress, New, etc.)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// resolveBucketStorageRef discovers S3 credentials from a Bucket storageRef
// It follows this flow:
// 1. Get the Bucket resource (apps.cozystack.io/v1alpha1 or objectstorage.k8s.io/v1alpha1)
// 2. Find the BucketAccess that references this bucket via bucketClaimName
// 3. Get the secret from BucketAccess.spec.credentialsSecretName
// 4. Decode BucketInfo from secret.data.BucketInfo and extract S3 credentials
func (r *BackupJobReconciler) resolveBucketStorageRef(ctx context.Context, storageRef corev1.TypedLocalObjectReference, namespace string, logger log.Logger) (*S3Credentials, error) {
	// Step 1: Get the Bucket resource
	// Try apps.cozystack.io first, then fall back to objectstorage.k8s.io
	bucket := &unstructured.Unstructured{}
	bucket.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   storageRef.APIGroup,
		Version: "v1alpha1",
		Kind:    storageRef.Kind,
	})

	bucketKey := client.ObjectKey{Name: storageRef.Name}
	// Bucket in apps.cozystack.io is namespaced, objectstorage.k8s.io is cluster-scoped
	if storageRef.APIGroup == "apps.cozystack.io" || storageRef.APIGroup == "apps.coyzstack.io" {
		bucketKey.Namespace = namespace
	}

	if err := r.Get(ctx, bucketKey, bucket); err != nil {
		return nil, fmt.Errorf("failed to get Bucket %s: %w", storageRef.Name, err)
	}

	// Step 2: Determine the bucket claim name
	// For apps.cozystack.io Bucket, the BucketClaim name is typically the same as the Bucket name
	// or follows a pattern. Based on the templates, it's usually the Release.Name which equals the Bucket name
	bucketClaimName := storageRef.Name

	// Step 3: Find BucketAccess by bucketClaimName
	bucketAccessList := &unstructured.UnstructuredList{}
	bucketAccessList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "objectstorage.k8s.io",
		Version: "v1alpha1",
		Kind:    "BucketAccessList",
	})

	if err := r.List(ctx, bucketAccessList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list BucketAccess resources: %w", err)
	}

	var bucketAccess *unstructured.Unstructured
	for i := range bucketAccessList.Items {
		ba := &bucketAccessList.Items[i]
		baBucketClaimName, found, err := unstructured.NestedString(ba.Object, "spec", "bucketClaimName")
		if err != nil {
			continue
		}
		if found && baBucketClaimName == bucketClaimName {
			bucketAccess = ba
			break
		}
	}

	if bucketAccess == nil {
		return nil, fmt.Errorf("BucketAccess not found for bucketClaimName: %s in namespace: %s", bucketClaimName, namespace)
	}

	// Step 4: Get the secret name from BucketAccess
	secretName, found, err := unstructured.NestedString(bucketAccess.Object, "spec", "credentialsSecretName")
	if err != nil || !found {
		return nil, fmt.Errorf("failed to get credentialsSecretName from BucketAccess: %w", err)
	}

	// Step 5: Get the secret
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{Namespace: namespace, Name: secretName}
	if err := r.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s: %w", secretName, err)
	}

	// Step 6: Decode BucketInfo from secret.data.BucketInfo
	bucketInfoData, found := secret.Data["BucketInfo"]
	if !found {
		return nil, fmt.Errorf("BucketInfo key not found in secret %s", secretName)
	}

	// Decode base64
	decoded, err := base64.StdEncoding.DecodeString(string(bucketInfoData))
	if err != nil {
		return nil, fmt.Errorf("failed to decode BucketInfo from secret %s: %w", secretName, err)
	}

	// Parse JSON
	var info bucketInfo
	if err := json.Unmarshal(decoded, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal BucketInfo from secret %s: %w", secretName, err)
	}

	// Step 7: Extract and return S3 credentials
	creds := &S3Credentials{
		BucketName:      info.Spec.BucketName,
		Endpoint:        info.Spec.SecretS3.Endpoint,
		Region:          info.Spec.SecretS3.Region,
		AccessKeyID:     info.Spec.SecretS3.AccessKeyID,
		AccessSecretKey: info.Spec.SecretS3.AccessSecretKey,
	}

	logger.Info("resolved S3 credentials from Bucket storageRef",
		"bucket", storageRef.Name,
		"bucketName", creds.BucketName,
		"endpoint", creds.Endpoint)

	return creds, nil
}

func (r *BackupJobReconciler) markBackupJobFailed(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, message string, logger log.Logger) (ctrl.Result, error) {
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

func (r *BackupJobReconciler) createVeleroBackup(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, name string, logger log.Logger) error {

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

	// Resolve StorageRef to get S3 credentials if it's a Bucket
	var storageLocation string = "default"
	if backupJob.Spec.StorageRef.Kind == "Bucket" {
		creds, err := r.resolveBucketStorageRef(ctx, backupJob.Spec.StorageRef, backupJob.Namespace, logger)
		if err != nil {
			return fmt.Errorf("failed to resolve Bucket storageRef: %w", err)
		}

		// TODO: Create or reference a Velero BackupStorageLocation using the discovered credentials
		// For now, we'll use "default" but log the discovered credentials
		logger.Info("discovered S3 credentials from Bucket storageRef",
			"bucketName", creds.BucketName,
			"endpoint", creds.Endpoint,
			"region", creds.Region)
		// Note: The actual BackupStorageLocation should be created/configured separately
		// or we should create it here dynamically. For now, using "default" as placeholder.
	}

	// Create a Velero Backup (velero.io/v1) using unstructured object
	// Only backup VirtualMachine resources
	spec := map[string]interface{}{
		"includedNamespaces": []string{backupJob.Namespace},
		"includedResources":   []string{"virtualmachines.kubevirt.io"},
		"storageLocation":     storageLocation,
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

func (r *BackupJobReconciler) createBackupResource(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, veleroBackup *unstructured.Unstructured, logger log.Logger) (*backupsv1alpha1.Backup, error) {
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
