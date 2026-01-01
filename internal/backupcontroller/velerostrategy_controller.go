package backupcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"

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

const (
	defaultRequeueAfter             = 5 * time.Second
	defaultActiveJobPollingInterval = defaultRequeueAfter
	// Velero requires API objects and secrets to be in the cozy-velero namespace
	veleroNamespace      = "cozy-velero"
	virtualMachinePrefix = "virtual-machine-"
)

func storageS3SecretName(namespace, backupJobName string) string {
	return fmt.Sprintf("backup-%s-%s-s3-credentials", namespace, backupJobName)
}

func boolPtr(b bool) *bool {
	return &b
}

func (r *BackupJobReconciler) reconcileVelero(ctx context.Context, j *backupsv1alpha1.BackupJob) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Velero strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	// If already completed, no need to reconcile
	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		logger.Debug("BackupJob already completed, skipping", "phase", j.Status.Phase)
		return ctrl.Result{}, nil
	}

	// For now implemented backup logic for apps.cozystack.io VirtualMachine only
	logger.Debug("validating BackupJob spec",
		"applicationRef", fmt.Sprintf("%s/%s", j.Spec.ApplicationRef.APIGroup, j.Spec.ApplicationRef.Kind),
		"storageRef", fmt.Sprintf("%s/%s", j.Spec.StorageRef.APIGroup, j.Spec.StorageRef.Kind))

	if j.Spec.ApplicationRef.Kind != "VirtualMachine" {
		logger.Error(nil, "Unsupported application type", "kind", j.Spec.ApplicationRef.Kind)
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Unsupported application type: %s", j.Spec.ApplicationRef.Kind))
	}
	if j.Spec.ApplicationRef.APIGroup == nil || *j.Spec.ApplicationRef.APIGroup != "apps.cozystack.io" {
		logger.Error(nil, "Unsupported application APIGroup", "apiGroup", j.Spec.ApplicationRef.APIGroup, "expected", "apps.cozystack.io")
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Unsupported application APIGroup: %v, expected apps.cozystack.io", j.Spec.ApplicationRef.APIGroup))
	}

	if j.Spec.StorageRef.Kind != "Bucket" {
		logger.Error(nil, "Unsupported storage type", "kind", j.Spec.StorageRef.Kind)
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Unsupported storage type: %s", j.Spec.StorageRef.Kind))
	}
	if j.Spec.StorageRef.APIGroup == nil || *j.Spec.StorageRef.APIGroup != "apps.cozystack.io" {
		logger.Error(nil, "Unsupported storage APIGroup", "apiGroup", j.Spec.StorageRef.APIGroup, "expected", "apps.cozystack.io")
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Unsupported storage APIGroup: %v, expected apps.cozystack.io", j.Spec.StorageRef.APIGroup))
	}

	logger.Debug("BackupJob spec validation passed")

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

	// Step 2: Resolve inputs - Read Strategy, Storage, Application, optionally Plan
	logger.Debug("fetching Velero strategy", "strategyName", j.Spec.StrategyRef.Name)
	veleroStrategy := &strategyv1alpha1.Velero{}
	if err := r.Get(ctx, client.ObjectKey{Name: j.Spec.StrategyRef.Name}, veleroStrategy); err != nil {
		if errors.IsNotFound(err) {
			logger.Error(err, "Velero strategy not found", "strategyName", j.Spec.StrategyRef.Name)
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Velero strategy not found: %s", j.Spec.StrategyRef.Name))
		}
		logger.Error(err, "failed to get Velero strategy")
		return ctrl.Result{}, err
	}
	logger.Debug("fetched Velero strategy", "strategyName", veleroStrategy.Name)

	// Step 3: Execute backup logic
	// Check if we already created a Velero Backup
	// Use human-readable timestamp: YYYY-MM-DD-HH-MM-SS
	if j.Status.StartedAt == nil {
		logger.Error(nil, "StartedAt is nil after status update, this should not happen")
		return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
	}
	timestamp := j.Status.StartedAt.Time.Format("2006-01-02-15-04-05")
	veleroBackupName := fmt.Sprintf("%s-%s-%s", j.Namespace, j.Name, timestamp)
	logger.Debug("checking for existing Velero Backup", "veleroBackupName", veleroBackupName, "namespace", veleroNamespace)
	veleroBackup := &velerov1.Backup{}
	veleroBackupKey := client.ObjectKey{Namespace: veleroNamespace, Name: veleroBackupName}

	if err := r.Get(ctx, veleroBackupKey, veleroBackup); err != nil {
		if errors.IsNotFound(err) {
			// Create Velero Backup
			logger.Debug("Velero Backup not found, creating new one", "veleroBackupName", veleroBackupName)
			if err := r.createVeleroBackup(ctx, j, veleroBackupName); err != nil {
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
			logger.Debug("created Velero Backup, requeuing", "veleroBackupName", veleroBackupName)
			// Requeue to check status
			return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
		}
		logger.Error(err, "failed to get Velero Backup")
		return ctrl.Result{}, err
	}
	logger.Debug("found existing Velero Backup", "veleroBackupName", veleroBackupName, "phase", veleroBackup.Status.Phase)

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
			backup, err := r.createBackupResource(ctx, j, veleroBackup)
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

// resolveBucketStorageRef discovers S3 credentials from a Bucket storageRef
// It follows this flow:
// 1. Get the Bucket resource (apps.cozystack.io/v1alpha1)
// 2. Find the BucketAccess that references this bucket
// 3. Get the secret from BucketAccess.spec.credentialsSecretName
// 4. Decode BucketInfo from secret.data.BucketInfo and extract S3 credentials
func (r *BackupJobReconciler) resolveBucketStorageRef(ctx context.Context, storageRef corev1.TypedLocalObjectReference, namespace string) (*S3Credentials, error) {
	logger := getLogger(ctx)

	// Step 1: Get the Bucket resource
	bucket := &unstructured.Unstructured{}
	bucket.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   *storageRef.APIGroup,
		Version: "v1alpha1",
		Kind:    storageRef.Kind,
	})

	if *storageRef.APIGroup != "apps.cozystack.io" {
		return nil, fmt.Errorf("Unsupported storage APIGroup: %v, expected apps.cozystack.io", storageRef.APIGroup)
	}
	bucketKey := client.ObjectKey{Namespace: namespace, Name: storageRef.Name}

	if err := r.Get(ctx, bucketKey, bucket); err != nil {
		return nil, fmt.Errorf("failed to get Bucket %s: %w", storageRef.Name, err)
	}

	// Step 2: Determine the bucket claim name
	// For apps.cozystack.io Bucket, the BucketClaim name is typically the same as the Bucket name
	// or follows a pattern. Based on the templates, it's usually the Release.Name which equals the Bucket name
	bucketName := storageRef.Name

	// Step 3: Get BucketAccess by name (assuming BucketAccess name matches bucketName)
	bucketAccess := &unstructured.Unstructured{}
	bucketAccess.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "objectstorage.k8s.io",
		Version: "v1alpha1",
		Kind:    "BucketAccess",
	})

	bucketAccessKey := client.ObjectKey{Name: "bucket-" + bucketName, Namespace: namespace}
	if err := r.Get(ctx, bucketAccessKey, bucketAccess); err != nil {
		return nil, fmt.Errorf("failed to get BucketAccess %s in namespace %s: %w", bucketName, namespace, err)
	}

	// Step 4: Get the secret name from BucketAccess
	secretName, found, err := unstructured.NestedString(bucketAccess.Object, "spec", "credentialsSecretName")
	if err != nil {
		return nil, fmt.Errorf("failed to get credentialsSecretName from BucketAccess: %w", err)
	}
	if !found || secretName == "" {
		return nil, fmt.Errorf("credentialsSecretName not found in BucketAccess %s", bucketAccessKey.Name)
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

	// Parse JSON value
	var info bucketInfo
	if err := json.Unmarshal(bucketInfoData, &info); err != nil {
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

	logger.Debug("resolved S3 credentials from Bucket storageRef",
		"bucket", storageRef.Name,
		"bucketName", creds.BucketName,
		"endpoint", creds.Endpoint)

	return creds, nil
}

// createS3CredsForVelero creates or updates a Kubernetes Secret containing
// Velero S3 credentials in the format expected by Velero's cloud-credentials plugin.
func (r *BackupJobReconciler) createS3CredsForVelero(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, creds *S3Credentials) error {
	logger := getLogger(ctx)
	secretName := storageS3SecretName(backupJob.Namespace, backupJob.Name)
	secretNamespace := veleroNamespace

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: secretNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"cloud": fmt.Sprintf(`[default]
aws_access_key_id=%s
aws_secret_access_key=%s

services = seaweed-s3
[services seaweed-s3]
s3 =
    endpoint_url = %s
`, creds.AccessKeyID, creds.AccessSecretKey, creds.Endpoint),
		},
	}

	foundSecret := &corev1.Secret{}
	secretKey := client.ObjectKey{Name: secretName, Namespace: secretNamespace}
	err := r.Get(ctx, secretKey, foundSecret)
	if err != nil && errors.IsNotFound(err) {
		// Create the Secret
		if err := r.Create(ctx, secret); err != nil {
			r.Recorder.Event(backupJob, corev1.EventTypeWarning, "SecretCreationFailed",
				fmt.Sprintf("Failed to create Velero credentials secret %s/%s: %v", secretNamespace, secretName, err))
			return fmt.Errorf("failed to create Velero credentials secret: %w", err)
		}
		logger.Debug("created Velero credentials secret", "secret", secretName)
		r.Recorder.Event(backupJob, corev1.EventTypeNormal, "SecretCreated",
			fmt.Sprintf("Created Velero credentials secret %s/%s", secretNamespace, secretName))
	} else if err == nil {
		// Update if necessary - only update if the secret data has actually changed
		// Compare the new secret data with existing secret data
		existingData := foundSecret.Data
		if existingData == nil {
			existingData = make(map[string][]byte)
		}
		newData := make(map[string][]byte)
		for k, v := range secret.StringData {
			newData[k] = []byte(v)
		}

		// Check if data has changed
		dataChanged := false
		if len(existingData) != len(newData) {
			dataChanged = true
		} else {
			for k, newVal := range newData {
				existingVal, exists := existingData[k]
				if !exists || !reflect.DeepEqual(existingVal, newVal) {
					dataChanged = true
					break
				}
			}
		}

		if dataChanged {
			foundSecret.StringData = secret.StringData
			foundSecret.Data = nil // Clear .Data so .StringData will be used
			if err := r.Update(ctx, foundSecret); err != nil {
				r.Recorder.Event(backupJob, corev1.EventTypeWarning, "SecretUpdateFailed",
					fmt.Sprintf("Failed to update Velero credentials secret %s/%s: %v", secretNamespace, secretName, err))
				return fmt.Errorf("failed to update Velero credentials secret: %w", err)
			}
			logger.Debug("updated Velero credentials secret", "secret", secretName)
			r.Recorder.Event(backupJob, corev1.EventTypeNormal, "SecretUpdated",
				fmt.Sprintf("Updated Velero credentials secret %s/%s", secretNamespace, secretName))
		} else {
			logger.Debug("Velero credentials secret data unchanged, skipping update", "secret", secretName)
		}
	} else if err != nil {
		return fmt.Errorf("error checking for existing Velero credentials secret: %w", err)
	}

	return nil
}

// createBackupStorageLocation creates or updates a Velero BackupStorageLocation resource.
func (r *BackupJobReconciler) createBackupStorageLocation(ctx context.Context, bsl *velerov1.BackupStorageLocation) error {
	logger := getLogger(ctx)
	foundBSL := &velerov1.BackupStorageLocation{}
	bslKey := client.ObjectKey{Name: bsl.Name, Namespace: bsl.Namespace}

	err := r.Get(ctx, bslKey, foundBSL)
	if err != nil && errors.IsNotFound(err) {
		// Create the BackupStorageLocation
		if err := r.Create(ctx, bsl); err != nil {
			return fmt.Errorf("failed to create BackupStorageLocation: %w", err)
		}
		logger.Debug("created BackupStorageLocation", "name", bsl.Name, "namespace", bsl.Namespace)
	} else if err == nil {
		// Update if necessary - use patch to avoid conflicts with Velero's status updates
		// Only update if the spec has actually changed
		if !reflect.DeepEqual(foundBSL.Spec, bsl.Spec) {
			// Retry on conflict since Velero may be updating status concurrently
			for i := 0; i < 3; i++ {
				if err := r.Get(ctx, bslKey, foundBSL); err != nil {
					return fmt.Errorf("failed to get BackupStorageLocation for update: %w", err)
				}
				foundBSL.Spec = bsl.Spec
				if err := r.Update(ctx, foundBSL); err != nil {
					if errors.IsConflict(err) && i < 2 {
						logger.Debug("conflict updating BackupStorageLocation, retrying", "attempt", i+1)
						time.Sleep(100 * time.Millisecond)
						continue
					}
					return fmt.Errorf("failed to update BackupStorageLocation: %w", err)
				}
				logger.Debug("updated BackupStorageLocation", "name", bsl.Name, "namespace", bsl.Namespace)
				return nil
			}
		} else {
			logger.Debug("BackupStorageLocation spec unchanged, skipping update", "name", bsl.Name, "namespace", bsl.Namespace)
		}
	} else if err != nil {
		return fmt.Errorf("error checking for existing BackupStorageLocation: %w", err)
	}

	return nil
}

// createVolumeSnapshotLocation creates or updates a Velero VolumeSnapshotLocation resource.
func (r *BackupJobReconciler) createVolumeSnapshotLocation(ctx context.Context, vsl *velerov1.VolumeSnapshotLocation) error {
	logger := getLogger(ctx)
	foundVSL := &velerov1.VolumeSnapshotLocation{}
	vslKey := client.ObjectKey{Name: vsl.Name, Namespace: vsl.Namespace}

	err := r.Get(ctx, vslKey, foundVSL)
	if err != nil && errors.IsNotFound(err) {
		// Create the VolumeSnapshotLocation
		if err := r.Create(ctx, vsl); err != nil {
			return fmt.Errorf("failed to create VolumeSnapshotLocation: %w", err)
		}
		logger.Debug("created VolumeSnapshotLocation", "name", vsl.Name, "namespace", vsl.Namespace)
	} else if err == nil {
		// Update if necessary - only update if the spec has actually changed
		if !reflect.DeepEqual(foundVSL.Spec, vsl.Spec) {
			// Retry on conflict since Velero may be updating status concurrently
			for i := 0; i < 3; i++ {
				if err := r.Get(ctx, vslKey, foundVSL); err != nil {
					return fmt.Errorf("failed to get VolumeSnapshotLocation for update: %w", err)
				}
				foundVSL.Spec = vsl.Spec
				if err := r.Update(ctx, foundVSL); err != nil {
					if errors.IsConflict(err) && i < 2 {
						logger.Debug("conflict updating VolumeSnapshotLocation, retrying", "attempt", i+1)
						time.Sleep(100 * time.Millisecond)
						continue
					}
					return fmt.Errorf("failed to update VolumeSnapshotLocation: %w", err)
				}
				logger.Debug("updated VolumeSnapshotLocation", "name", vsl.Name, "namespace", vsl.Namespace)
				return nil
			}
		} else {
			logger.Debug("VolumeSnapshotLocation spec unchanged, skipping update", "name", vsl.Name, "namespace", vsl.Namespace)
		}
	} else if err != nil {
		return fmt.Errorf("error checking for existing VolumeSnapshotLocation: %w", err)
	}

	return nil
}

func (r *BackupJobReconciler) markBackupJobFailed(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, message string) (ctrl.Result, error) {
	logger := getLogger(ctx)
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
	logger.Debug("BackupJob failed", "message", message)
	return ctrl.Result{}, nil
}

func (r *BackupJobReconciler) createVeleroBackup(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, name string) error {
	logger := getLogger(ctx)
	logger.Debug("createVeleroBackup called", "backupJob", backupJob.Name, "veleroBackupName", name)

	// Resolve StorageRef to get S3 credentials if it's a Bucket
	// Prefix with namespace to avoid conflicts in cozy-velero namespace
	var locationName string = fmt.Sprintf("%s-%s", backupJob.Namespace, backupJob.Name)
	if backupJob.Spec.StorageRef.Kind == "Bucket" {
		logger.Debug("resolving Bucket storageRef", "storageRef", backupJob.Spec.StorageRef.Name)
		creds, err := r.resolveBucketStorageRef(ctx, backupJob.Spec.StorageRef, backupJob.Namespace)
		if err != nil {
			logger.Error(err, "failed to resolve Bucket storageRef")
			return fmt.Errorf("failed to resolve Bucket storageRef: %w", err)
		}

		logger.Debug("discovered S3 credentials from Bucket storageRef",
			"bucketName", creds.BucketName,
			"endpoint", creds.Endpoint,
			"region", creds.Region)

		if err := r.createS3CredsForVelero(ctx, backupJob, creds); err != nil {
			return fmt.Errorf("failed to create or update Velero credentials secret: %w", err)
		}
		// Dynamically create a Velero BackupStorageLocation and VolumeSnapshotLocation using discovered credentials.

		// BackupStorageLocation manifest
		// Note: Cannot set owner reference for cross-namespace resources
		bsl := &velerov1.BackupStorageLocation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      locationName,
				Namespace: veleroNamespace,
			},
			Spec: velerov1.BackupStorageLocationSpec{
				Provider: "aws",
				StorageType: velerov1.StorageType{
					ObjectStorage: &velerov1.ObjectStorageLocation{
						Bucket: creds.BucketName,
					},
				},
				Config: map[string]string{
					"checksumAlgorithm": "",
					"profile":           "default",
					"s3ForcePathStyle":  "true",
					"s3Url":             creds.Endpoint,
				},
				Credential: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: storageS3SecretName(backupJob.Namespace, backupJob.Name),
					},
					Key: "cloud",
				},
			},
		}

		// Create or update the BackupStorageLocation
		if err := r.createBackupStorageLocation(ctx, bsl); err != nil {
			logger.Error(err, "failed to create or update BackupStorageLocation for Velero")
			r.Recorder.Event(backupJob, corev1.EventTypeWarning, "BackupStorageLocationCreationFailed",
				fmt.Sprintf("Failed to create or update BackupStorageLocation %s/%s: %v", veleroNamespace, locationName, err))
			return fmt.Errorf("failed to create or update Velero BackupStorageLocation: %w", err)
		}
		r.Recorder.Event(backupJob, corev1.EventTypeNormal, "BackupStorageLocationCreated",
			fmt.Sprintf("Created or updated BackupStorageLocation %s/%s", veleroNamespace, locationName))

		// VolumeSnapshotLocation manifest
		// Note: Cannot set owner reference for cross-namespace resources
		vsl := &velerov1.VolumeSnapshotLocation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      locationName,
				Namespace: veleroNamespace,
			},
			Spec: velerov1.VolumeSnapshotLocationSpec{
				Provider: "aws",
				Credential: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: storageS3SecretName(backupJob.Namespace, backupJob.Name),
					},
					Key: "cloud",
				},
				Config: map[string]string{
					"region":  creds.Region,
					"profile": "default",
				},
			},
		}

		// Create or update the VolumeSnapshotLocation
		if err := r.createVolumeSnapshotLocation(ctx, vsl); err != nil {
			logger.Error(err, "failed to create or update VolumeSnapshotLocation for Velero")
			r.Recorder.Event(backupJob, corev1.EventTypeWarning, "VolumeSnapshotLocationCreationFailed",
				fmt.Sprintf("Failed to create or update VolumeSnapshotLocation %s/%s: %v", veleroNamespace, locationName, err))
			return fmt.Errorf("failed to create or update Velero VolumeSnapshotLocation: %w", err)
		}
		r.Recorder.Event(backupJob, corev1.EventTypeNormal, "VolumeSnapshotLocationCreated",
			fmt.Sprintf("Created or updated VolumeSnapshotLocation %s/%s", veleroNamespace, locationName))
	}

	// Create a Velero Backup (velero.io/v1) using typed object
	// Now implemented only for backup of VirtualMachine resources
	// Note: Cannot set owner reference for cross-namespace resources
	veleroBackup := &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: veleroNamespace,
		},
		Spec: velerov1.BackupSpec{
			IncludedNamespaces:      []string{backupJob.Namespace},
			IncludedResources:       []string{"virtualmachines.kubevirt.io"},
			SnapshotVolumes:         boolPtr(true),
			StorageLocation:         locationName,
			VolumeSnapshotLocations: []string{locationName},
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": virtualMachinePrefix + backupJob.Spec.ApplicationRef.Name,
				},
			},
		},
	}

	if err := r.Create(ctx, veleroBackup); err != nil {
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

func (r *BackupJobReconciler) createBackupResource(ctx context.Context, backupJob *backupsv1alpha1.BackupJob, veleroBackup *velerov1.Backup) (*backupsv1alpha1.Backup, error) {
	logger := getLogger(ctx)
	// Extract artifact information from Velero Backup
	// Create a basic artifact referencing the Velero backup
	artifact := &backupsv1alpha1.BackupArtifact{
		URI: fmt.Sprintf("velero://%s/%s", backupJob.Namespace, veleroBackup.Name),
	}

	// Get takenAt from Velero Backup creation timestamp or status
	takenAt := metav1.Now()
	if veleroBackup.Status.StartTimestamp != nil {
		takenAt = *veleroBackup.Status.StartTimestamp
	} else if !veleroBackup.CreationTimestamp.IsZero() {
		takenAt = veleroBackup.CreationTimestamp
	}

	// Extract driver metadata (e.g., Velero backup name)
	driverMetadata := map[string]string{
		"velero.io/backup-name":      veleroBackup.Name,
		"velero.io/backup-namespace": veleroBackup.Namespace,
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
					Controller: boolPtr(true),
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

	logger.Debug("created Backup resource", "name", backup.Name)
	return backup, nil
}
