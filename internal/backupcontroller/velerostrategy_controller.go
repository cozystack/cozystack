package backupcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

const (
	defaultRequeueAfter                 = 5 * time.Second
	defaultActiveJobPollingInterval     = defaultRequeueAfter
	defaultRestoreRequeueAfter          = 5 * time.Second
	defaultActiveRestorePollingInterval = defaultRestoreRequeueAfter
	// Velero requires API objects and secrets to be in the cozy-velero namespace
	veleroNamespace                  = "cozy-velero"
	veleroBackupNameMetadataKey      = "velero.io/backup-name"
	veleroBackupNamespaceMetadataKey = "velero.io/backup-namespace"

	// Annotation key for persisting underlying resources on the Velero Backup object
	underlyingResourcesAnnotation = "backups.cozystack.io/underlying-resources"

	// VM-specific constants
	vmInstanceKind        = "VMInstance"
	vmDiskAppKind         = "VMDisk"
	vmNamePrefix          = "vm-instance-"
	vmDiskNamePrefix      = "vm-disk-"
	appKindLabel          = "apps.cozystack.io/application.kind"
	appNameLabel          = "apps.cozystack.io/application.name"
	vmPodNameLabel        = "vm.kubevirt.io/name"
	ovnIPAnnotation       = "ovn.kubernetes.io/ip_address"
	ovnMACAnnotation      = "ovn.kubernetes.io/mac_address"
	cdiAllowClaimAdoption = "cdi.kubevirt.io/allowClaimAdoption"
)

func stringPtr(s string) *string {
	return &s
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}

// boolDefault returns the value of a *bool pointer, or the given default if nil.
func boolDefault(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}

// CommonRestoreOptions contains driver-agnostic restore options shared across
// all application kinds.
type CommonRestoreOptions struct {
	// TargetNamespace is the namespace to restore into. When set (and differs
	// from the backup namespace), a cross-namespace restore (copy) is performed
	// using Velero's namespaceMapping.
	TargetNamespace string `json:"targetNamespace,omitempty"`
	// FailIfTargetExists makes the restore fail if the target resource already
	// exists. Defaults to true when omitted.
	FailIfTargetExists *bool `json:"failIfTargetExists,omitempty"`
}

// RestoreOptions is the typed representation of RestoreJob.Spec.Options for the
// Velero driver. The struct is deserialized from runtime.RawExtension and used
// for all application kinds. VMInstance-specific fields (KeepOriginalPVC,
// KeepOriginalIpAndMac) are only effective when the application kind is VMInstance.
type RestoreOptions struct {
	CommonRestoreOptions `json:",inline"`
	// KeepOriginalPVC renames the original PVC to <name>-orig-<hash> before restore.
	// Only effective for in-place VMInstance restore (no targetNamespace). Defaults to true when omitted.
	KeepOriginalPVC *bool `json:"keepOriginalPVC,omitempty"`
	// KeepOriginalIpAndMac preserves the original IP and MAC address via OVN
	// annotations. Only effective for VMInstance restores. Defaults to true when omitted.
	KeepOriginalIpAndMac *bool `json:"keepOriginalIpAndMac,omitempty"`
}

// GetFailIfTargetExists returns the effective value (default: true).
func (o *CommonRestoreOptions) GetFailIfTargetExists() bool {
	return boolDefault(o.FailIfTargetExists, true)
}

// GetKeepOriginalPVC returns the effective value (default: true).
func (o *RestoreOptions) GetKeepOriginalPVC() bool {
	return boolDefault(o.KeepOriginalPVC, true)
}

// GetKeepOriginalIpAndMac returns the effective value (default: true).
func (o *RestoreOptions) GetKeepOriginalIpAndMac() bool {
	return boolDefault(o.KeepOriginalIpAndMac, true)
}

// parseRestoreOptions deserializes RestoreJob.Spec.Options into RestoreOptions.
// Returns zero-value RestoreOptions if options is nil.
func parseRestoreOptions(opts *runtime.RawExtension) (RestoreOptions, error) {
	var ro RestoreOptions
	if opts == nil || len(opts.Raw) == 0 {
		return ro, nil
	}
	if err := json.Unmarshal(opts.Raw, &ro); err != nil {
		return ro, fmt.Errorf("failed to parse restore options: %w", err)
	}
	return ro, nil
}

// restoreTarget holds the resolved target namespace and app identity for a restore operation.
type restoreTarget struct {
	Namespace string
	AppName   string
	AppKind   string
	IsCopy    bool // true when targetNamespace differs from backup namespace
	IsRenamed bool // true when target app name differs from source app name
}

// resolveRestoreTarget computes the effective restore target from RestoreJob, Backup, and options.
func resolveRestoreTarget(restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, opts RestoreOptions) restoreTarget {
	targetNS := backup.Namespace
	isCopy := false
	if opts.TargetNamespace != "" && opts.TargetNamespace != backup.Namespace {
		targetNS = opts.TargetNamespace
		isCopy = true
	}
	targetAppName := backup.Spec.ApplicationRef.Name
	if restoreJob.Spec.TargetApplicationRef != nil && restoreJob.Spec.TargetApplicationRef.Name != "" {
		targetAppName = restoreJob.Spec.TargetApplicationRef.Name
	}
	targetAppKind := backup.Spec.ApplicationRef.Kind
	if restoreJob.Spec.TargetApplicationRef != nil && restoreJob.Spec.TargetApplicationRef.Kind != "" {
		targetAppKind = restoreJob.Spec.TargetApplicationRef.Kind
	}
	return restoreTarget{
		Namespace: targetNS,
		AppName:   targetAppName,
		AppKind:   targetAppKind,
		IsCopy:    isCopy,
		IsRenamed: targetAppName != backup.Spec.ApplicationRef.Name,
	}
}

// vmInstanceResources contains VM-specific underlying resources discovered during backup.
type vmInstanceResources struct {
	DataVolumes []backupsv1alpha1.DataVolumeResource `json:"dataVolumes,omitempty"`
	IP          string                                `json:"ip,omitempty"`
	MAC         string                                `json:"mac,omitempty"`
}

// marshalUnderlyingResources serializes application-specific data into a
// runtime.RawExtension suitable for Backup.Status.UnderlyingResources.
func marshalUnderlyingResources(data interface{}) (*runtime.RawExtension, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: raw}, nil
}

// getVMInstanceResources extracts VMInstance-specific resources from the opaque blob.
// The caller is responsible for checking that the application kind is VMInstance
// (via backup.Spec.ApplicationRef.Kind) before calling this function.
// Returns nil if ur is nil or has no VM-specific data.
func getVMInstanceResources(ur *runtime.RawExtension) *vmInstanceResources {
	if ur == nil || len(ur.Raw) == 0 {
		return nil
	}
	var res vmInstanceResources
	if err := json.Unmarshal(ur.Raw, &res); err != nil {
		return nil
	}
	if len(res.DataVolumes) == 0 && res.IP == "" && res.MAC == "" {
		return nil
	}
	return &res
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
		message := formatVeleroBackupFailureMessageForBackupJob(ctx, r.Client, veleroBackup)
		return r.markBackupJobFailed(ctx, j, message)
	}

	// Still in progress (InProgress, New, etc.)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// collectUnderlyingResources discovers resources associated with an application
// that need to be backed up and restored. Returns a map keyed by application kind.
// Returns nil if the application type has no underlying resources to collect.
func (r *BackupJobReconciler) collectUnderlyingResources(ctx context.Context, app *unstructured.Unstructured, appKind, ns string) (*runtime.RawExtension, error) {
	logger := getLogger(ctx)

	if appKind != vmInstanceKind {
		logger.Debug("application is not a VMInstance, skipping underlying resource collection", "kind", appKind)
		return nil, nil
	}

	appName := app.GetName()

	// Extract disk names from VMInstance spec.disks[].name
	disks, found, err := unstructured.NestedSlice(app.Object, "spec", "disks")
	if err != nil {
		return nil, fmt.Errorf("failed to read spec.disks from application: %w", err)
	}

	var dataVolumes []backupsv1alpha1.DataVolumeResource
	if found {
		for _, d := range disks {
			disk, ok := d.(map[string]interface{})
			if !ok {
				continue
			}
			name, ok := disk["name"].(string)
			if !ok || name == "" {
				continue
			}
			dataVolumes = append(dataVolumes, backupsv1alpha1.DataVolumeResource{
				DataVolumeName:  vmDiskNamePrefix + name,
				ApplicationName: name,
			})
		}
	}
	logger.Debug("collected dataVolumes from VMInstance", "count", len(dataVolumes), "appName", appName)

	// Find VM Pod to extract OVN IP/MAC addresses
	vmName := vmNamePrefix + appName
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(ns),
		client.MatchingLabels{vmPodNameLabel: vmName},
	); err != nil {
		logger.Error(err, "failed to list VM pods for IP/MAC collection", "vmName", vmName)
		// Non-fatal: we can still proceed without IP/MAC
	}

	var ip, mac string
	if len(podList.Items) > 0 {
		pod := podList.Items[0]
		ip = pod.Annotations[ovnIPAnnotation]
		mac = pod.Annotations[ovnMACAnnotation]
		logger.Debug("collected OVN network info from VM pod", "ip", ip, "mac", mac, "pod", pod.Name)
	} else {
		logger.Debug("no VM pod found for OVN info", "vmName", vmName)
	}

	if len(dataVolumes) == 0 && ip == "" && mac == "" {
		return nil, nil
	}

	return marshalUnderlyingResources(vmInstanceResources{
		DataVolumes: dataVolumes,
		IP:          ip,
		MAC:         mac,
	})
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

	// Collect underlying resources (VM disks, IP/MAC)
	underlyingResources, err := r.collectUnderlyingResources(ctx, app, backupJob.Spec.ApplicationRef.Kind, backupJob.Namespace)
	if err != nil {
		logger.Error(err, "failed to collect underlying resources, proceeding without them")
		// Non-fatal: proceed with backup even if collection fails
	}

	templateContext := map[string]interface{}{
		"Application": app.Object,
		"Parameters":  resolved.Parameters,
	}

	veleroBackupSpec, err := template.Template(&strategy.Spec.Template.Spec, templateContext)
	if err != nil {
		return err
	}

	// Add label selectors for underlying VMDisk HelmReleases
	if vmRes := getVMInstanceResources(underlyingResources); vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			veleroBackupSpec.OrLabelSelectors = append(veleroBackupSpec.OrLabelSelectors, &metav1.LabelSelector{
				MatchLabels: map[string]string{
					appKindLabel: vmDiskAppKind,
					appNameLabel: dv.ApplicationName,
				},
			})
		}
		if len(vmRes.DataVolumes) > 0 {
			logger.Debug("added VMDisk label selectors to Velero backup", "count", len(vmRes.DataVolumes))
		}
	}

	// Serialize underlying resources as annotation to persist across reconcile cycles
	annotations := map[string]string{}
	if underlyingResources != nil && len(underlyingResources.Raw) > 0 {
		annotations[underlyingResourcesAnnotation] = string(underlyingResources.Raw)
	}

	veleroBackup := &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s.%s-", backupJob.Namespace, backupJob.Name),
			Namespace:    veleroNamespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      backupJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: backupJob.Namespace,
			},
			Annotations: annotations,
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

	// Read underlying resources from Velero Backup annotation
	var underlyingResources *runtime.RawExtension
	if urJSON, ok := veleroBackup.Annotations[underlyingResourcesAnnotation]; ok && urJSON != "" {
		underlyingResources = &runtime.RawExtension{Raw: []byte(urJSON)}
	}

	// Note: No OwnerReferences set on Backup. The Backup must survive BackupJob deletion
	// so users don't lose their backup artifacts when cleaning up completed jobs.
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupJob.Name,
			Namespace: backupJob.Namespace,
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: backupJob.Spec.ApplicationRef,
			StrategyRef:    resolved.StrategyRef,
			TakenAt:        takenAt,
			DriverMetadata: driverMetadata,
		},
		Status: backupsv1alpha1.BackupStatus{
			Phase:               backupsv1alpha1.BackupPhaseReady,
			Artifact:            artifact,
			UnderlyingResources: underlyingResources,
		},
	}

	if backupJob.Spec.PlanRef != nil {
		backup.Spec.PlanRef = backupJob.Spec.PlanRef
	}

	if err := r.Create(ctx, backup); err != nil {
		logger.Error(err, "failed to create Backup resource")
		return nil, err
	}

	logger.Debug("created Backup resource", "name", backup.Name,
		"hasUnderlyingResources", underlyingResources != nil)
	return backup, nil
}

// reconcileVeleroRestore handles restore operations for Velero strategy.
func (r *RestoreJobReconciler) reconcileVeleroRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Velero strategy restore", "restorejob", restoreJob.Name, "backup", backup.Name)

	// Parse restore options from the opaque blob
	restoreOpts, err := parseRestoreOptions(restoreJob.Spec.Options)
	if err != nil {
		return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("invalid restore options: %v", err))
	}

	target := resolveRestoreTarget(restoreJob, backup, restoreOpts)
	logger.Debug("resolved restore target", "targetNS", target.Namespace, "targetApp", target.AppName, "isCopy", target.IsCopy)

	// Validate: target namespace must exist for cross-namespace copies
	if target.IsCopy {
		targetNS := &corev1.Namespace{}
		if err := r.Get(ctx, client.ObjectKey{Name: target.Namespace}, targetNS); err != nil {
			if errors.IsNotFound(err) {
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
					"target namespace %q does not exist; create it before requesting a cross-namespace restore",
					target.Namespace))
			}
			return ctrl.Result{}, fmt.Errorf("failed to check target namespace %q: %w", target.Namespace, err)
		}
	}

	// Validate: same-namespace restore with a different app name is not supported
	// due to Velero DataUpload always writing to PVCs with the original name.
	if !target.IsCopy && target.AppName != backup.Spec.ApplicationRef.Name {
		return r.markRestoreJobFailed(ctx, restoreJob,
			"restoring to the same namespace with a different application name is not supported "+
				"due to Velero DataUpload limitations: data is always uploaded to PVCs with the original name. "+
				"Use options.targetNamespace to restore into a different namespace")
	}

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
		// For copy restores, enforce failIfTargetExists before touching anything.
		// In-place restores are excluded: the source application is expected to exist
		// and will be halted/overwritten deliberately.
		if target.IsCopy && restoreOpts.GetFailIfTargetExists() {
			targetHRName := helmReleaseNameForApp(target.AppKind, target.AppName)
			exists, err := r.targetHelmReleaseExists(ctx, target.AppKind, targetHRName, target.Namespace)
			if err != nil {
				logger.Error(err, "failed to check whether target HelmRelease exists")
				return ctrl.Result{}, err
			}
			if exists {
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf(
					"target application %q already exists in namespace %q; "+
						"set options.failIfTargetExists=false to overwrite",
					target.AppName, target.Namespace))
			}
		}

		// Resolve underlying resources once; prefer Backup status, fall back to Velero annotation.
		ur := r.resolveUnderlyingResourcesForRestore(ctx, backup, veleroBackupName)

		// Pre-restore: graceful shutdown, suspend HRs, rename PVCs (skipped for copy)
		ready, result, err := r.prepareForRestore(ctx, restoreJob, backup, ur, target, restoreOpts)
		if err != nil {
			return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("pre-restore preparation failed: %v", err))
		}
		if !ready {
			logger.Debug("pre-restore preparation in progress, requeuing")
			return result, nil
		}

		// Create Velero Restore
		logger.Debug("Velero Restore not found, creating new one")
		if err := r.createVeleroRestore(ctx, restoreJob, backup, veleroStrategy, veleroBackupName, ur, target, restoreOpts); err != nil {
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
		// Post-restore: rename resources if target app name differs from source.
		// Velero resource modifiers cannot change metadata.name, so we do it after restore.
		if target.IsRenamed {
			if err := r.postRestoreRename(ctx, restoreJob, backup, target); err != nil {
				r.cleanupResourceModifierConfigMaps(ctx, restoreJob)
				return r.markRestoreJobFailed(ctx, restoreJob, fmt.Sprintf("post-restore rename failed: %v", err))
			}
		}

		// Clean up resource modifier ConfigMaps now that the restore is complete.
		r.cleanupResourceModifierConfigMaps(ctx, restoreJob)

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
		r.cleanupResourceModifierConfigMaps(ctx, restoreJob)
		message := fmt.Sprintf("Velero Restore failed with phase: %s", phase)
		if veleroRestore.Status.FailureReason != "" {
			message = fmt.Sprintf("%s: %s", message, veleroRestore.Status.FailureReason)
		}
		return r.markRestoreJobFailed(ctx, restoreJob, message)
	}

	// Still in progress (InProgress, New, etc.)
	return ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
}

// Velero resource modifier types (local mirrors of the internal Velero types).

type resourceModifiers struct {
	Version               string                 `yaml:"version"`
	ResourceModifierRules []resourceModifierRule `yaml:"resourceModifierRules"`
}

type resourceModifierRule struct {
	Conditions   resourceModifierConditions `yaml:"conditions"`
	MergePatches []mergePatch               `yaml:"mergePatches,omitempty"`
	Patches      []jsonPatch                `yaml:"patches,omitempty"`
}

type resourceModifierConditions struct {
	GroupResource     string   `yaml:"groupResource"`
	ResourceNameRegex string   `yaml:"resourceNameRegex,omitempty"`
	Namespaces        []string `yaml:"namespaces,omitempty"`
}

type mergePatch struct {
	PatchData string `yaml:"patchData"`
}

type jsonPatch struct {
	Operation string `yaml:"operation"`
	Path      string `yaml:"path"`
	Value     string `yaml:"value,omitempty"`
}

// marshalPatchData marshals an arbitrary object to YAML for use as
// mergePatch.PatchData in Velero resource modifiers.
func marshalPatchData(v interface{}) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("failed to marshal patch data: %w", err)
	}
	return string(b), nil
}

// createResourceModifiersConfigMap creates a Velero resource modifiers ConfigMap
// that patches VM resources during restore:
//   - PVC adoption: always adds cdi.kubevirt.io/allowClaimAdoption=true to all
//     restored PVCs so CDI can adopt them when a HelmRelease of VMDisk recreates a DV.
//   - OVN IP/MAC: sets OVN annotations on the VirtualMachine for correct ssh access to restored VM.
func (r *RestoreJobReconciler) createResourceModifiersConfigMap(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, ur *runtime.RawExtension, target restoreTarget, opts RestoreOptions) (*corev1.ConfigMap, error) {
	logger := getLogger(ctx)

	targetNS := target.Namespace

	var rules []resourceModifierRule

	// PVC adoption: allow CDI to adopt restored PVCs when the HelmRelease recreates a DV.
	pvcPatch, err := marshalPatchData(map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{
				cdiAllowClaimAdoption: "true",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	rules = append(rules, resourceModifierRule{
		Conditions: resourceModifierConditions{
			GroupResource:     "persistentvolumeclaims",
			ResourceNameRegex: ".*",
			Namespaces:        []string{targetNS},
		},
		MergePatches: []mergePatch{{PatchData: pvcPatch}},
	})

	// For cross-namespace restore: strip Velero's dynamic PV restore selector and
	// volumeName from PVCs so the storage provisioner can dynamically provision new PVs.
	// Without this, Velero's CSI PVCAction adds spec.selector with a velero.io/dynamic-pv-restore
	// label that prevents dynamic provisioning when PVs are not included in the restore.
	// Uses merge patch (null values) instead of JSON Patch remove to avoid RFC 6902
	// failures when the fields don't exist on the PVC (e.g. statically provisioned PVCs).
	if target.IsCopy {
		pvcStripPatch, err := marshalPatchData(map[string]interface{}{
			"spec": map[string]interface{}{
				"selector":   nil,
				"volumeName": nil,
			},
		})
		if err != nil {
			return nil, err
		}
		rules = append(rules, resourceModifierRule{
			Conditions: resourceModifierConditions{
				GroupResource:     "persistentvolumeclaims",
				ResourceNameRegex: ".*",
				Namespaces:        []string{targetNS},
			},
			MergePatches: []mergePatch{{PatchData: pvcStripPatch}},
		})
	}

	// OVN IP/MAC annotations on VirtualMachine for correct network identity after restore.
	// Only applied when keepOriginalIpAndMac is true; for restore-to-copy the copy
	// should get new IP/MAC from the network to avoid conflicts.
	if opts.GetKeepOriginalIpAndMac() {
		if vmRes := getVMInstanceResources(ur); vmRes != nil && (vmRes.IP != "" || vmRes.MAC != "") {
			ovnAnnotations := map[string]string{}
			if vmRes.IP != "" {
				ovnAnnotations[ovnIPAnnotation] = vmRes.IP
			}
			if vmRes.MAC != "" {
				ovnAnnotations[ovnMACAnnotation] = vmRes.MAC
			}
			vmPatch, err := marshalPatchData(map[string]interface{}{
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{
							"annotations": ovnAnnotations,
						},
					},
				},
			})
			if err != nil {
				return nil, err
			}
			rules = append(rules, resourceModifierRule{
				Conditions: resourceModifierConditions{
					GroupResource:     "virtualmachines.kubevirt.io",
					ResourceNameRegex: ".*",
					Namespaces:        []string{targetNS},
				},
				MergePatches: []mergePatch{{PatchData: vmPatch}},
			})
		}
	}

	rulesYAML, err := yaml.Marshal(resourceModifiers{
		Version:               "v1",
		ResourceModifierRules: rules,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resource modifier rules: %w", err)
	}

	cmName := fmt.Sprintf("restore-modifiers-%s-%s", restoreJob.Namespace, restoreJob.Name)
	// Truncate name to fit Kubernetes 253-char limit
	if len(cmName) > 253 {
		cmName = cmName[:253]
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: veleroNamespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
			},
		},
		Data: map[string]string{
			"resource-modifier-rules.yaml": string(rulesYAML),
		},
	}

	if err := r.Create(ctx, cm); err != nil {
		if errors.IsAlreadyExists(err) {
			// ConfigMap already exists (e.g. RestoreJob recreated with same name).
			// Update its data to reflect the current backup's underlying resources.
			existing := &corev1.ConfigMap{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: veleroNamespace, Name: cmName}, existing); err != nil {
				return nil, fmt.Errorf("failed to get existing resourceModifiers ConfigMap: %w", err)
			}
			existing.Data = cm.Data
			if err := r.Update(ctx, existing); err != nil {
				return nil, fmt.Errorf("failed to update existing resourceModifiers ConfigMap: %w", err)
			}
			logger.Debug("updated existing resourceModifiers ConfigMap", "name", cmName)
			return existing, nil
		}
		return nil, fmt.Errorf("failed to create resourceModifiers ConfigMap: %w", err)
	}

	logger.Debug("created resourceModifiers ConfigMap", "name", cm.Name, "namespace", cm.Namespace)
	return cm, nil
}

// resolveUnderlyingResourcesForRestore returns underlying resources for symmetric
// restore label selectors. Velero Backup annotation is used when Backup.status was empty
// (e.g. CRD without underlyingResources in schema).
func (r *RestoreJobReconciler) resolveUnderlyingResourcesForRestore(ctx context.Context, backup *backupsv1alpha1.Backup, veleroBackupName string) *runtime.RawExtension {
	if backup.Status.UnderlyingResources != nil && len(backup.Status.UnderlyingResources.Raw) > 0 {
		return backup.Status.UnderlyingResources
	}
	vb := &velerov1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: veleroNamespace, Name: veleroBackupName}, vb); err != nil {
		return backup.Status.UnderlyingResources
	}
	if urJSON, ok := vb.Annotations[underlyingResourcesAnnotation]; ok && urJSON != "" {
		return &runtime.RawExtension{Raw: []byte(urJSON)}
	}
	return backup.Status.UnderlyingResources
}

// GVRs used during pre-restore preparation.
var (
	helmReleaseGVR    = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	virtualMachineGVR = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	vmiGVR            = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"}
	dataVolumeGVR     = schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"}
)

// helmReleaseNameForApp returns the HelmRelease name for the given application
// kind and name. Returns empty string for unsupported kinds.
func helmReleaseNameForApp(appKind, appName string) string {
	switch appKind {
	case vmInstanceKind:
		return vmNamePrefix + appName
	case vmDiskAppKind:
		return vmDiskNamePrefix + appName
	default:
		return ""
	}
}

// targetHelmReleaseExists returns true when a HelmRelease with the given name
// already exists in namespace. Returns false for unsupported application kinds
// (those where helmReleaseNameForApp returns "").
func (r *RestoreJobReconciler) targetHelmReleaseExists(ctx context.Context, appKind, hrName, namespace string) (bool, error) {
	if hrName == "" {
		return false, nil
	}
	_, err := r.Resource(helmReleaseGVR).Namespace(namespace).Get(ctx, hrName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// shortHash returns the first 4 hex characters of sha256(input).
func shortHash(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:4]
}

// prepareForRestore performs graceful pre-restore cleanup:
//  1. Suspends HelmReleases that belong to the backup scope.
//  2. Halts the VirtualMachine (sets spec.runStrategy=Halted).
//  3. Waits for the VMI to disappear (graceful shutdown complete).
//  4. Deletes DataVolumes so CDI doesn't recreate PVCs after rename.
//  5. Renames existing PVCs to <name>-orig-<hash> so Velero can create fresh
//     ones via Data Movement.
//
// Failures in individual steps are non-fatal: missing resources are expected
// (e.g. restore requested when app was already deleted). Each action emits
// a Kubernetes Event on the RestoreJob for observability.
//
// postRestoreRename renames VMInstance HelmRelease after Velero Restore completes.
// Velero resource modifiers cannot change metadata.name, so this step creates
// a new HelmRelease with the target name and deletes the old one.
// Flux will reconcile the renamed HelmRelease and recreate downstream resources
// (VM, VMI) with the new name.
func (r *RestoreJobReconciler) postRestoreRename(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, target restoreTarget) error {
	logger := getLogger(ctx)
	sourceAppName := backup.Spec.ApplicationRef.Name
	sourceHRName := vmNamePrefix + sourceAppName
	targetHRName := vmNamePrefix + target.AppName

	logger.Debug("post-restore rename", "from", sourceHRName, "to", targetHRName, "namespace", target.Namespace)

	// Get the restored HelmRelease with the original name
	hrClient := r.Resource(helmReleaseGVR).Namespace(target.Namespace)
	oldHR, err := hrClient.Get(ctx, sourceHRName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Debug("source HelmRelease not found, skipping rename", "name", sourceHRName)
			return nil
		}
		return fmt.Errorf("failed to get HelmRelease %s: %w", sourceHRName, err)
	}

	// Create new HelmRelease with the target name
	newHR := oldHR.DeepCopy()
	newHR.SetName(targetHRName)
	newHR.SetResourceVersion("")
	newHR.SetUID("")
	newHR.SetCreationTimestamp(metav1.Time{})
	newHR.SetManagedFields(nil)
	newHR.SetGeneration(0)

	// Update labels
	labels := newHR.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[appNameLabel] = target.AppName
	labels["app.kubernetes.io/instance"] = targetHRName
	labels["helm.toolkit.fluxcd.io/name"] = targetHRName
	newHR.SetLabels(labels)

	// Remove Velero restore annotations/labels that tie it to the old restore
	annotations := newHR.GetAnnotations()
	delete(annotations, "velero.io/restore-name")
	newHR.SetAnnotations(annotations)

	// Clear status so Flux reconciles fresh
	unstructured.RemoveNestedField(newHR.Object, "status")

	if _, err := hrClient.Create(ctx, newHR, metav1.CreateOptions{}); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Debug("target HelmRelease already exists, skipping create", "name", targetHRName)
		} else {
			return fmt.Errorf("failed to create renamed HelmRelease %s: %w", targetHRName, err)
		}
	} else {
		r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PostRestoreRename",
			fmt.Sprintf("Created renamed HelmRelease %s (from %s)", targetHRName, sourceHRName))
	}

	// Delete the old HelmRelease
	if err := hrClient.Delete(ctx, sourceHRName, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete old HelmRelease %s: %w", sourceHRName, err)
	}
	r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PostRestoreRename",
		fmt.Sprintf("Deleted old HelmRelease %s", sourceHRName))

	logger.Debug("post-restore rename complete", "from", sourceHRName, "to", targetHRName)
	return nil
}

// Returns true when preparation is complete and the Velero Restore can be created.
// Returns false (with a requeue) when still waiting for VM shutdown.
func (r *RestoreJobReconciler) prepareForRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, ur *runtime.RawExtension, target restoreTarget, opts RestoreOptions) (ready bool, result ctrl.Result, err error) {
	// For restore-to-copy, skip all source-app preparation.
	// The source application remains untouched; we're restoring a copy into another namespace.
	if target.IsCopy {
		r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
			"Restore to copy: skipping source application preparation")
		return true, ctrl.Result{}, nil
	}

	ns := restoreJob.Namespace
	appName := backup.Spec.ApplicationRef.Name
	appKind := backup.Spec.ApplicationRef.Kind
	origSuffix := "-orig-" + shortHash(restoreJob.Name)

	// --- Step 1: Suspend HelmReleases ---
	vmRes := getVMInstanceResources(ur)
	hrNames := []string{}
	if appKind == vmInstanceKind {
		hrNames = append(hrNames, vmNamePrefix+appName)
	}
	if vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			hrNames = append(hrNames, dv.DataVolumeName)
		}
	}
	for _, hrName := range hrNames {
		if err := r.suspendHelmRelease(ctx, ns, hrName); err != nil {
			r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
				fmt.Sprintf("Failed to suspend HelmRelease %s: %v", hrName, err))
		} else {
			r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
				fmt.Sprintf("Suspended HelmRelease %s", hrName))
		}
	}

	// --- Step 2: Halt VM and wait for shutdown ---
	if appKind == vmInstanceKind {
		vmName := vmNamePrefix + appName
		halted, err := r.haltVirtualMachine(ctx, ns, vmName)
		if err != nil {
			r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
				fmt.Sprintf("Failed to halt VM %s: %v", vmName, err))
			// Non-fatal: proceed even if halting fails (VM might not exist)
		} else if !halted {
			r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
				fmt.Sprintf("Waiting for VM %s to shut down", vmName))
			return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
		} else {
			r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
				fmt.Sprintf("VM %s is halted", vmName))
		}
	}

	// --- Step 3: Rename PVCs to <name>-orig-<hash> ---
	// Must happen BEFORE deleting DVs: the PVC has an ownerReference to the DV,
	// so deleting the DV first would cascade-delete the PVC via garbage collection.
	// Only when keepOriginalPVC is true (default for in-place restore).
	if opts.GetKeepOriginalPVC() && vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			if err := r.renamePVC(ctx, restoreJob, ns, dv.DataVolumeName, dv.DataVolumeName+origSuffix); err != nil {
				r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
					fmt.Sprintf("Failed to keep old PVC %s: %v", dv.DataVolumeName, err))
			}
		}
	}

	// --- Step 4: Delete DataVolumes so CDI doesn't recreate PVCs ---
	if vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			if err := r.deleteDataVolume(ctx, ns, dv.DataVolumeName); err != nil {
				r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
					fmt.Sprintf("Failed to delete DataVolume %s: %v", dv.DataVolumeName, err))
			} else {
				r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
					fmt.Sprintf("Deleted DataVolume %s", dv.DataVolumeName))
			}
		}
	}

	r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore", "Pre-restore preparation complete")
	return true, ctrl.Result{}, nil
}

// suspendHelmRelease sets spec.suspend=true on a HelmRelease.
func (r *RestoreJobReconciler) suspendHelmRelease(ctx context.Context, ns, name string) error {
	hr, err := r.Resource(helmReleaseGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	suspended, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend")
	if suspended {
		return nil
	}
	if err := unstructured.SetNestedField(hr.Object, true, "spec", "suspend"); err != nil {
		return err
	}
	if _, err := r.Resource(helmReleaseGVR).Namespace(ns).Update(ctx, hr, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

// haltVirtualMachine sets runStrategy=Halted and returns true when the VMI is gone.
func (r *RestoreJobReconciler) haltVirtualMachine(ctx context.Context, ns, vmName string) (bool, error) {
	vm, err := r.Resource(virtualMachineGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	currentStrategy, _, _ := unstructured.NestedString(vm.Object, "spec", "runStrategy")
	if currentStrategy != "Halted" {
		if err := unstructured.SetNestedField(vm.Object, "Halted", "spec", "runStrategy"); err != nil {
			return false, err
		}
		if _, err := r.Resource(virtualMachineGVR).Namespace(ns).Update(ctx, vm, metav1.UpdateOptions{}); err != nil {
			return false, err
		}
	}

	// VMI gone = shutdown complete
	_, err = r.Resource(vmiGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// deleteDataVolume deletes a DataVolume so CDI doesn't recreate the PVC after rename.
// Uses Orphan propagation to avoid cascade-deleting the PVC that the DV owns via
// ownerReference. Without this, keepOriginalPVC=false would silently destroy the
// original PVC through garbage collection instead of leaving it for Velero to overwrite.
func (r *RestoreJobReconciler) deleteDataVolume(ctx context.Context, ns, name string) error {
	orphan := metav1.DeletePropagationOrphan
	err := r.Resource(dataVolumeGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &orphan,
	})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// renamePVC preserves an existing PVC by rebinding it under a new name.
// The original PVC is deleted and a new one pointing to the same PV is created.
// Missing resources are silently skipped (non-fatal).
func (r *RestoreJobReconciler) renamePVC(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, ns, oldName, newName string) error {
	logger := getLogger(ctx)

	// Check if already renamed
	existingNew := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: newName}, existingNew); err == nil {
		return nil
	}

	// Get the original PVC
	oldPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: oldName}, oldPVC); err != nil {
		if errors.IsNotFound(err) {
			return nil // nothing to rename
		}
		return err
	}

	pvName := oldPVC.Spec.VolumeName
	if pvName == "" {
		logger.Debug("PVC not bound, deleting", "name", oldName)
		return r.Delete(ctx, oldPVC)
	}

	// Patch PV reclaim policy to Retain so it survives PVC deletion
	pv := &corev1.PersistentVolume{}
	if err := r.Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
		return fmt.Errorf("failed to get PV %s: %w", pvName, err)
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
		if err := r.Update(ctx, pv); err != nil {
			return fmt.Errorf("failed to set Retain policy on PV %s: %w", pvName, err)
		}
	}

	// Create the new -orig PVC first (unbound, just the object)
	newPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newName,
			Namespace: ns,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      oldPVC.Spec.AccessModes,
			Resources:        oldPVC.Spec.Resources,
			StorageClassName: oldPVC.Spec.StorageClassName,
			VolumeMode:       oldPVC.Spec.VolumeMode,
			VolumeName:       pvName,
		},
	}
	if err := r.Create(ctx, newPVC); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create -orig PVC %s: %w", newName, err)
	}
	// Re-read to get UID for the claimRef
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: newName}, newPVC); err != nil {
		return fmt.Errorf("failed to get -orig PVC %s: %w", newName, err)
	}

	// Delete the original PVC
	if err := r.Delete(ctx, oldPVC); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete PVC %s: %w", oldName, err)
	}

	// Point the PV's claimRef directly to the new -orig PVC.
	// This is atomic — no window where the PV is Available for other PVCs to grab.
	if err := r.Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
		return fmt.Errorf("failed to re-fetch PV %s: %w", pvName, err)
	}
	pv.Spec.ClaimRef = &corev1.ObjectReference{
		APIVersion: "v1",
		Kind:       "PersistentVolumeClaim",
		Namespace:  ns,
		Name:       newName,
		UID:        newPVC.UID,
	}
	if err := r.Update(ctx, pv); err != nil {
		return fmt.Errorf("failed to rebind PV %s to %s: %w", pvName, newName, err)
	}

	r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
		fmt.Sprintf("Keep old PVC %s as %s (PV: %s)", oldName, newName, pvName))
	return nil
}

// createVeleroRestore creates a Velero Restore resource.
func (r *RestoreJobReconciler) createVeleroRestore(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, strategy *strategyv1alpha1.Velero, veleroBackupName string, ur *runtime.RawExtension, target restoreTarget, opts RestoreOptions) error {
	logger := getLogger(ctx)
	logger.Debug("createVeleroRestore called", "strategy", strategy.Name, "veleroBackupName", veleroBackupName, "targetNS", target.Namespace, "isCopy", target.IsCopy)

	// For restore template context, always use the source (backup) namespace and app name.
	// The strategy template uses includedNamespaces and orLabelSelectors to select
	// resources from the backup tarball, which are stored under the source namespace
	// and labeled with the source app name.
	// Velero's namespaceMapping handles redirecting to the target namespace;
	// resource modifiers handle renaming when the target app name differs.
	templateContext := map[string]interface{}{
		"Application": map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":      backup.Spec.ApplicationRef.Name,
				"namespace": backup.Namespace,
			},
			"kind": backup.Spec.ApplicationRef.Kind,
		},
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

	// For restore-to-copy, set Velero namespaceMapping to redirect resources
	// from the source namespace to the target namespace.
	if target.IsCopy {
		if veleroRestoreSpec.NamespaceMapping == nil {
			veleroRestoreSpec.NamespaceMapping = make(map[string]string)
		}
		veleroRestoreSpec.NamespaceMapping[backup.Namespace] = target.Namespace
		logger.Debug("set namespaceMapping on Velero Restore", "from", backup.Namespace, "to", target.Namespace)
	}

	// Match backup: add OR selectors for each underlying VMDisk so restore applies the same
	// scope as the intended backup (see createVeleroBackup).
	if vmRes := getVMInstanceResources(ur); vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			veleroRestoreSpec.OrLabelSelectors = append(veleroRestoreSpec.OrLabelSelectors, &metav1.LabelSelector{
				MatchLabels: map[string]string{
					appKindLabel: vmDiskAppKind,
					appNameLabel: dv.ApplicationName,
				},
			})
		}
		if len(vmRes.DataVolumes) > 0 {
			logger.Debug("added VMDisk label selectors to Velero restore", "count", len(vmRes.DataVolumes))
		}
	}

	// Create resourceModifiers ConfigMap
	resourceModifierCM, err := r.createResourceModifiersConfigMap(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		return fmt.Errorf("failed to create resourceModifiers ConfigMap: %w", err)
	}
	if resourceModifierCM != nil {
		veleroRestoreSpec.ResourceModifier = &corev1.TypedLocalObjectReference{
			APIGroup: stringPtr(""),
			Kind:     "ConfigMap",
			Name:     resourceModifierCM.Name,
		}
		logger.Debug("set resourceModifier on Velero Restore", "configMap", resourceModifierCM.Name)
	}

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

// dataUploadListGVK is the API version shipped with Velero data mover CRDs (see velero datauploads CRD).
var dataUploadListGVK = schema.GroupVersionKind{Group: "velero.io", Version: "v2alpha1", Kind: "DataUploadList"}

// formatVeleroBackupFailureMessageForBackupJob builds a BackupJob status message from Velero Backup
// status plus failed DataUpload resources (CSI data mover), similar to `velero backup describe`.
func formatVeleroBackupFailureMessageForBackupJob(ctx context.Context, c client.Client, veleroBackup *velerov1.Backup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Velero Backup failed with phase %s", veleroBackup.Status.Phase)
	if fr := strings.TrimSpace(veleroBackup.Status.FailureReason); fr != "" {
		fmt.Fprintf(&b, ": %s", fr)
	}
	if len(veleroBackup.Status.ValidationErrors) > 0 {
		fmt.Fprintf(&b, "; validation: %v", veleroBackup.Status.ValidationErrors)
	}
	if h := veleroBackup.Status.HookStatus; h != nil && h.HooksFailed > 0 {
		fmt.Fprintf(&b, "; hooks failed %d/%d", h.HooksFailed, h.HooksAttempted)
	}
	if veleroBackup.Status.BackupItemOperationsFailed > 0 {
		fmt.Fprintf(&b, "; async item operations failed %d (completed %d, attempted %d)",
			veleroBackup.Status.BackupItemOperationsFailed,
			veleroBackup.Status.BackupItemOperationsCompleted,
			veleroBackup.Status.BackupItemOperationsAttempted)
	}
	b.WriteString(appendFailedDataUploadMessages(ctx, c, veleroBackup.Name))
	return b.String()
}

func appendFailedDataUploadMessages(ctx context.Context, c client.Client, veleroBackupName string) string {
	ul := unstructured.UnstructuredList{}
	ul.SetGroupVersionKind(dataUploadListGVK)
	if err := c.List(ctx, &ul, client.InNamespace(veleroNamespace)); err != nil {
		return ""
	}
	prefix := veleroBackupName + "-"
	var b strings.Builder
	for _, item := range ul.Items {
		if !strings.HasPrefix(item.GetName(), prefix) {
			continue
		}
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if phase != "Failed" {
			continue
		}
		msg, _, _ := unstructured.NestedString(item.Object, "status", "message")
		if strings.TrimSpace(msg) == "" {
			msg = "(empty status.message)"
		}
		srcPVC, _, _ := unstructured.NestedString(item.Object, "spec", "sourcePVC")
		if srcPVC != "" {
			fmt.Fprintf(&b, "; DataUpload %s failed for PVC %s: %s", item.GetName(), srcPVC, msg)
		} else {
			fmt.Fprintf(&b, "; DataUpload %s failed: %s", item.GetName(), msg)
		}
	}
	return b.String()
}
