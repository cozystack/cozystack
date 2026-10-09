package backupcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
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
	"k8s.io/client-go/util/retry"
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
func (l loggerWithDebug) Debug(msg string, keysAndValues ...any) {
	l.Logger.V(1).Info(msg, keysAndValues...)
}

const (
	defaultRequeueAfter                 = 5 * time.Second
	defaultActiveJobPollingInterval     = defaultRequeueAfter
	defaultRestoreRequeueAfter          = 5 * time.Second
	defaultActiveRestorePollingInterval = defaultRestoreRequeueAfter

	// Condition recorded on a RestoreJob before prepareForRestore first changes
	// the target. From then on a failure has to say what preparation left
	// behind, and its LastTransitionTime starts the preparation deadline.
	restoreCondPreparationStarted = "PreparationStarted"
	// How long preparation may wait (VM shutdown, Pods releasing the disks, the
	// DataVolume and a recreated PVC going away) before the RestoreJob fails.
	restorePreparationDeadline = 30 * time.Minute
	// Velero requires API objects and secrets to be in the cozy-velero namespace
	veleroNamespace                  = "cozy-velero"
	veleroBackupNameMetadataKey      = "velero.io/backup-name"
	veleroBackupNamespaceMetadataKey = "velero.io/backup-namespace"

	// Annotation key for persisting underlying resources on the Velero Backup object
	underlyingResourcesAnnotation = "backups.cozystack.io/underlying-resources"

	// keepOriginalPVC moves a disk's PV to Retain so it survives the deletion
	// of its PVC. The policy it had is kept here, on the PV and on the
	// <name>-orig-<hash> PVC, because nothing in the controller restores it:
	// whoever deletes the -orig PVC must set it back first, or a PV that was
	// Delete stays Retain and its volume is never reclaimed.
	originalReclaimPolicyAnnotation = "backups.cozystack.io/original-reclaim-policy"
	// The -orig suffix hashes the RestoreJob name, and a name can be reused
	// once the previous RestoreJob is deleted, leaving its -orig PVC behind.
	// The UID tells a rename this RestoreJob started apart from that one.
	origPVCRestoreJobUIDAnnotation = "backups.cozystack.io/restore-job-uid"

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
// for all application kinds. KeepOriginalPVC applies to VMInstance and VMDisk
// targets; KeepOriginalIpAndMac only to VMInstance.
type RestoreOptions struct {
	CommonRestoreOptions `json:",inline"`
	// KeepOriginalPVC renames the original PVC to <name>-orig-<hash> before restore.
	// Only effective for an in-place VMInstance or VMDisk restore (no targetNamespace). Defaults to true when omitted.
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
	IP          string                               `json:"ip,omitempty"`
	MAC         string                               `json:"mac,omitempty"`
}

// marshalUnderlyingResources serializes application-specific data into a
// runtime.RawExtension suitable for Backup.Status.UnderlyingResources.
func marshalUnderlyingResources(data any) (*runtime.RawExtension, error) {
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
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
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
	appName := app.GetName()

	// A standalone VMDisk is its own underlying disk. Capturing it lets the
	// restore controller prepare it as it prepares a VMInstance's disks: keep
	// the live PVC aside and delete the DataVolume, so the restored PVC is
	// created fresh under the original name. The HelmRelease is suspended only
	// until Velero restores it; Flux then recreates the DataVolume and CDI
	// adopts the restored PVC through the allowClaimAdoption annotation the
	// restore's resource modifiers set. A standalone VMDisk has no VM, so there
	// is no IP/MAC to collect.
	if appKind == vmDiskAppKind {
		return marshalUnderlyingResources(vmInstanceResources{
			DataVolumes: []backupsv1alpha1.DataVolumeResource{{
				DataVolumeName:  vmDiskNamePrefix + appName,
				ApplicationName: appName,
			}},
		})
	}

	if appKind != vmInstanceKind {
		logger.Debug("application has no underlying resources to collect", "kind", appKind)
		return nil, nil
	}

	// Extract disk names from VMInstance spec.disks[].name
	disks, found, err := unstructured.NestedSlice(app.Object, "spec", "disks")
	if err != nil {
		return nil, fmt.Errorf("failed to read spec.disks from application: %w", err)
	}

	var dataVolumes []backupsv1alpha1.DataVolumeResource
	if found {
		for _, d := range disks {
			disk, ok := d.(map[string]any)
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

	templateContext := map[string]any{
		"Application": app.Object,
		"Parameters":  resolved.Parameters,
	}

	veleroBackupSpec, err := template.Template(&strategy.Spec.Template.Spec, templateContext)
	if err != nil {
		return err
	}

	// Add label selectors for a VMInstance's VMDisk HelmReleases. A VMDisk's own
	// strategy already selects the disk, and Velero rejects a spec that carries
	// both labelSelector and orLabelSelectors, so a VMDisk strategy written with
	// labelSelector must be left as it is.
	if vmRes := getVMInstanceResources(underlyingResources); vmRes != nil && backupJob.Spec.ApplicationRef.Kind == vmInstanceKind {
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

	// Validate: rename is only implemented for VMInstance. Failing here beats a
	// Succeeded RestoreJob whose application silently keeps the source name.
	if target.IsRenamed && !veleroRenameSupported(backup.Spec.ApplicationRef.Kind) {
		msg := fmt.Sprintf(
			"restoring %s %q under a different name (%q) is not supported by the Velero strategy: "+
				"Velero DataUpload always restores volumes under their original names and only a VMInstance "+
				"can adopt them. Omit targetApplicationRef.name to keep the source name",
			backup.Spec.ApplicationRef.Kind, backup.Spec.ApplicationRef.Name, target.AppName)
		if backup.Spec.ApplicationRef.Kind != vmDiskAppKind {
			msg += ", or use the application's engine-native strategy to restore into a new application"
		}
		if restoreJob.Status.StartedAt != nil {
			// The job predates this check: its Velero Restore may already have
			// restored the application under the source name.
			r.cleanupResourceModifierConfigMaps(ctx, restoreJob)
			msg += fmt.Sprintf(". This RestoreJob was already running, so %q may have been restored into namespace %q under its source name",
				backup.Spec.ApplicationRef.Name, target.Namespace)
		}
		return r.markRestoreJobFailed(ctx, restoreJob, msg)
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
			return r.requeueRestoreStrategyNotReady(ctx, restoreJob, backup.Spec.StrategyRef.Name)
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

		// Preparation spans several passes. Once it has changed the target, a
		// failure must name what it left behind instead of claiming nothing changed.
		failBeforeRestore := func(msg string) (ctrl.Result, error) {
			if meta.IsStatusConditionTrue(restoreJob.Status.Conditions, restoreCondPreparationStarted) {
				return r.markPreparedRestoreFailed(ctx, restoreJob, msg+preparedRestoreLeftovers(restoreJob, backup, ur, target, restoreOpts))
			}
			return r.markRestoreJobFailed(ctx, restoreJob, msg+"; nothing was changed")
		}

		// Preparation takes the target apart before Velero creates the Restore,
		// so check here that the Velero Backup can still be restored: an expired
		// or deleted one would leave the disk taken apart for nothing.
		if msg, err := r.veleroBackupUnrestorable(ctx, veleroBackupName); err != nil {
			return ctrl.Result{}, err
		} else if msg != "" {
			return failBeforeRestore(msg)
		}

		// Pre-restore: graceful shutdown, suspend HRs, rename PVCs (skipped for copy)
		ready, result, err := r.prepareForRestore(ctx, restoreJob, backup, ur, target, restoreOpts)
		if err != nil {
			return failBeforeRestore(fmt.Sprintf("pre-restore preparation failed: %v", err))
		}
		if !ready {
			if cond := meta.FindStatusCondition(restoreJob.Status.Conditions, restoreCondPreparationStarted); cond != nil &&
				cond.Status == metav1.ConditionTrue && time.Since(cond.LastTransitionTime.Time) > restorePreparationDeadline {
				return failBeforeRestore(fmt.Sprintf("pre-restore preparation did not finish within %s; see the RestoreJob events for what it was waiting on", restorePreparationDeadline))
			}
			logger.Debug("pre-restore preparation in progress, requeuing")
			return result, nil
		}

		// Create Velero Restore
		logger.Debug("Velero Restore not found, creating new one")
		if err := r.createVeleroRestore(ctx, restoreJob, backup, veleroStrategy, veleroBackupName, ur, target, restoreOpts); err != nil {
			logger.Error(err, "failed to create Velero Restore")
			return r.markPreparedRestoreFailed(ctx, restoreJob, fmt.Sprintf("failed to create Velero Restore: %v", err)+
				preparedRestoreLeftovers(restoreJob, backup, ur, target, restoreOpts))
		}
		logger.Debug("created Velero Restore, requeuing")
		// Requeue to check status
		return ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
	}

	if len(veleroRestoreList.Items) > 1 {
		logger.Error(fmt.Errorf("too many Velero restores for RestoreJob"), "found more than one Velero Restore referencing a single RestoreJob as owner")
		return r.markPreparedRestoreFailed(ctx, restoreJob, "found multiple Velero Restores for this RestoreJob"+
			preparedRestoreLeftovers(restoreJob, backup, r.resolveUnderlyingResourcesForRestore(ctx, backup, veleroBackupName), target, restoreOpts))
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

	// Step 5: On failure. FailedValidation is terminal too; left to the polling
	// branch below it would keep a target that preparation already took apart
	// Running forever.
	if phase == string(velerov1.RestorePhaseFailed) || phase == string(velerov1.RestorePhasePartiallyFailed) ||
		phase == string(velerov1.RestorePhaseFailedValidation) {
		r.cleanupResourceModifierConfigMaps(ctx, restoreJob)
		message := fmt.Sprintf("Velero Restore failed with phase: %s", phase)
		if veleroRestore.Status.FailureReason != "" {
			message = fmt.Sprintf("%s: %s", message, veleroRestore.Status.FailureReason)
		}
		if len(veleroRestore.Status.ValidationErrors) > 0 {
			message = fmt.Sprintf("%s: %s", message, strings.Join(veleroRestore.Status.ValidationErrors, "; "))
		}
		return r.markPreparedRestoreFailed(ctx, restoreJob, message+
			preparedRestoreLeftovers(restoreJob, backup, r.resolveUnderlyingResourcesForRestore(ctx, backup, veleroBackupName), target, restoreOpts))
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
func marshalPatchData(v any) (string, error) {
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
	pvcPatch, err := marshalPatchData(map[string]any{
		"metadata": map[string]any{
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
		pvcStripPatch, err := marshalPatchData(map[string]any{
			"spec": map[string]any{
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
			vmPatch, err := marshalPatchData(map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"metadata": map[string]any{
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

// veleroRenameSupported reports whether postRestoreRename can restore the
// given application kind under a new name. Velero DataUpload writes volumes
// under their original names, and only a VMInstance re-attaches them after its
// HelmRelease is renamed: its disks keep their names and CDI adopts the restored
// PVCs. Every other kind (a standalone VMDisk, a database operator's Cluster)
// renders fresh volumes for the new name and comes up empty.
func veleroRenameSupported(appKind string) bool {
	return appKind == vmInstanceKind
}

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
// a Kubernetes Event on the RestoreJob for observability. The rename is the
// exception: the DataVolume deletion after it would hand a disk that was not
// kept to the restore, as keepOriginalPVC=false does. A failed rename holds
// the restore back and is retried, and an -orig name another PVC holds fails
// the restore.
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
	vmRes := restoreDisks(backup, ur)

	// A VMInstance is halted below, which releases its disks; a VMDisk target is
	// not. Renaming a PVC that a running VM still mounts leaves the original
	// Terminating behind pvc-protection, and Velero skips restoring a PVC that
	// still exists, so the restore would report success having moved nothing
	// while the VM loses its disk on the next restart. Refuse before changing
	// anything.
	if appKind != vmInstanceKind && vmRes != nil {
		inUse, leaving, err := r.podsMountingDisks(ctx, ns, vmRes.DataVolumes)
		if err != nil {
			r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
				fmt.Sprintf("Failed to check whether the disk is in use: %v", err))
			return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
		}
		if len(inUse) > 0 {
			return false, ctrl.Result{}, fmt.Errorf(
				"the disk is in use by %s; stop the VM that uses it, or restore that VMInstance instead, then create a new RestoreJob",
				strings.Join(inUse, ", "))
		}
		if len(leaving) > 0 {
			r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
				fmt.Sprintf("Waiting for %s to release the disk", strings.Join(leaving, ", ")))
			return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
		}
	}

	// Refuse a taken -orig name before anything is halted or deleted, rather
	// than fail halfway through a multi-disk rename.
	if opts.GetKeepOriginalPVC() && vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			origPVC, err := r.getOrigPVC(ctx, ns, dv.DataVolumeName+origSuffix)
			if err != nil {
				r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
					fmt.Sprintf("Failed to check PVC %s: %v", dv.DataVolumeName+origSuffix, err))
				return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
			}
			if err := foreignOrigPVC(origPVC, restoreJob); err != nil {
				return false, ctrl.Result{}, err
			}
		}
	}

	if !meta.IsStatusConditionTrue(restoreJob.Status.Conditions, restoreCondPreparationStarted) {
		before := restoreJob.DeepCopy()
		meta.SetStatusCondition(&restoreJob.Status.Conditions, metav1.Condition{
			Type:    restoreCondPreparationStarted,
			Status:  metav1.ConditionTrue,
			Reason:  "Preparing",
			Message: "suspending the target's HelmReleases and keeping its disks aside",
		})
		if err := r.Status().Patch(ctx, restoreJob, client.MergeFrom(before)); err != nil {
			return false, ctrl.Result{}, err
		}
	}

	// --- Step 1: Suspend HelmReleases ---
	hrNames := []string{}
	if appKind == vmInstanceKind {
		hrNames = append(hrNames, vmNamePrefix+appName)
	}
	if vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			hrNames = append(hrNames, dv.DataVolumeName)
		}
	}
	// A HelmRelease left running would have Flux and CDI recreate the
	// DataVolume and PVC under the data mover, so a failed suspend stops here.
	for _, hrName := range hrNames {
		if err := r.suspendHelmRelease(ctx, ns, hrName); err != nil {
			r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
				fmt.Sprintf("Failed to suspend HelmRelease %s: %v", hrName, err))
			return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
		}
		r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
			fmt.Sprintf("Suspended HelmRelease %s", hrName))
	}

	// --- Step 2: Halt VM and wait for shutdown ---
	if appKind == vmInstanceKind {
		vmName := vmNamePrefix + appName
		// haltVirtualMachine reports a missing VM as halted, so an error here is
		// a VM that may still be running on the disks.
		halted, err := r.haltVirtualMachine(ctx, ns, vmName)
		if err != nil {
			r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
				fmt.Sprintf("Failed to halt VM %s: %v", vmName, err))
			return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
		} else if !halted {
			r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
				fmt.Sprintf("Waiting for VM %s to shut down", vmName))
			return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
		} else {
			r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
				fmt.Sprintf("VM %s is halted", vmName))
		}
		// The VMI is gone before its launcher Pod is; renaming a PVC that Pod still
		// mounts would leave it Terminating behind pvc-protection.
		if vmRes != nil {
			inUse, leaving, err := r.podsMountingDisks(ctx, ns, vmRes.DataVolumes)
			if err != nil {
				r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
					fmt.Sprintf("Failed to check whether the disks are in use: %v", err))
				return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
			}
			if holders := append(inUse, leaving...); len(holders) > 0 {
				r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
					fmt.Sprintf("Waiting for %s to release the disks", strings.Join(holders, ", ")))
				return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
			}
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
				return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
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

	// --- Step 5: Clear a PVC CDI recreated under the original name ---
	// Between the rename and the DataVolume delete, CDI can recreate an empty PVC
	// for the still-present DataVolume. Velero skips restoring a PVC that exists,
	// so the restore would report success and leave the disk blank. The original
	// is kept under its -orig name by now, so a PVC under the original name is
	// such a copy and holds no data.
	if opts.GetKeepOriginalPVC() && vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			clear, err := r.clearRecreatedPVC(ctx, restoreJob, ns, dv.DataVolumeName)
			if err != nil {
				r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "PrepareForRestore",
					fmt.Sprintf("Failed to clear PVC %s: %v", dv.DataVolumeName, err))
			}
			if err != nil || !clear {
				return false, ctrl.Result{RequeueAfter: defaultRestoreRequeueAfter}, nil
			}
		}
	}

	r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore", "Pre-restore preparation complete")
	return true, ctrl.Result{}, nil
}

// clearRecreatedPVC reports whether the original name of a kept disk is free
// for Velero to restore into. It waits for the DataVolume to be gone, so CDI
// cannot recreate the PVC again, and deletes a PVC found under the name; the
// caller requeues until both are gone.
func (r *RestoreJobReconciler) clearRecreatedPVC(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, ns, name string) (bool, error) {
	if _, err := r.Resource(dataVolumeGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return false, nil
	} else if !errors.IsNotFound(err) {
		return false, err
	}
	// Uncached: the cache can still hold the original PVC the rename just deleted.
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.apiReader().Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, pvc); err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	if !pvc.DeletionTimestamp.IsZero() {
		return false, nil
	}
	if err := r.Delete(ctx, pvc); err != nil && !errors.IsNotFound(err) {
		return false, err
	}
	r.Recorder.Event(restoreJob, corev1.EventTypeNormal, "PrepareForRestore",
		fmt.Sprintf("Deleted PVC %s that CDI recreated after the original was kept aside", name))
	return false, nil
}

// markPreparedRestoreFailed fails a RestoreJob whose target was already
// prepared, and repeats the message as a Warning event, since recovering the
// target is left to the user.
func (r *RestoreJobReconciler) markPreparedRestoreFailed(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, message string) (ctrl.Result, error) {
	r.Recorder.Event(restoreJob, corev1.EventTypeWarning, "RestoreFailed", message)
	return r.markRestoreJobFailed(ctx, restoreJob, message)
}

// restoreDisks returns the disks an in-place restore prepares. A VMDisk Backup
// taken before its underlying resources were recorded has none, but its only
// disk is the VMDisk itself, named as collectUnderlyingResources names it.
func restoreDisks(backup *backupsv1alpha1.Backup, ur *runtime.RawExtension) *vmInstanceResources {
	if vmRes := getVMInstanceResources(ur); vmRes != nil {
		return vmRes
	}
	if backup.Spec.ApplicationRef.Kind != vmDiskAppKind {
		return nil
	}
	name := backup.Spec.ApplicationRef.Name
	return &vmInstanceResources{DataVolumes: []backupsv1alpha1.DataVolumeResource{{
		DataVolumeName:  vmDiskNamePrefix + name,
		ApplicationName: name,
	}}}
}

// preparedRestoreLeftovers describes, for the failure message of an in-place
// restore that fails after prepareForRestore ran, what preparation left behind.
// Nothing is rolled back automatically: resuming a disk's HelmRelease while its
// PVC is renamed and its DataVolume deleted would have CDI import a fresh disk
// under the original name, which looks healthy and hides that the data sits in
// the -orig- PVC.
func preparedRestoreLeftovers(restoreJob *backupsv1alpha1.RestoreJob, backup *backupsv1alpha1.Backup, ur *runtime.RawExtension, target restoreTarget, opts RestoreOptions) string {
	if target.IsCopy {
		return ""
	}
	vmRes := restoreDisks(backup, ur)
	var hrs, kept []string
	if backup.Spec.ApplicationRef.Kind == vmInstanceKind {
		hrs = append(hrs, vmNamePrefix+backup.Spec.ApplicationRef.Name)
	}
	if vmRes != nil {
		for _, dv := range vmRes.DataVolumes {
			hrs = append(hrs, dv.DataVolumeName)
			if opts.GetKeepOriginalPVC() {
				kept = append(kept, dv.DataVolumeName+"-orig-"+shortHash(restoreJob.Name))
			}
		}
	}
	if len(hrs) == 0 {
		return ""
	}
	msg := fmt.Sprintf("; preparation suspended HelmRelease %s", strings.Join(hrs, ", "))
	if len(kept) > 0 {
		msg += fmt.Sprintf(" and kept the original data in PVC %s", strings.Join(kept, ", "))
	}
	return msg + "; if Velero restored the HelmRelease it is resumed and the disk may have been recreated, so check what the disk holds before relying on it"
}

// podsMountingDisks returns "pod/<pod> (PVC <claim>)" for every Pod in ns that
// has not terminated and mounts one of the disks' PVCs, split into Pods that
// are running and Pods already being deleted. A terminated Pod no longer holds
// pvc-protection, so it does not count.
func (r *RestoreJobReconciler) podsMountingDisks(ctx context.Context, ns string, disks []backupsv1alpha1.DataVolumeResource) (inUse, leaving []string, err error) {
	claims := make(map[string]bool, len(disks))
	for _, dv := range disks {
		claims[dv.DataVolumeName] = true
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(ns)); err != nil {
		return nil, nil, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim == nil || !claims[v.PersistentVolumeClaim.ClaimName] {
				continue
			}
			holder := fmt.Sprintf("pod/%s (PVC %s)", pod.Name, v.PersistentVolumeClaim.ClaimName)
			if pod.DeletionTimestamp.IsZero() {
				inUse = append(inUse, holder)
			} else {
				leaving = append(leaving, holder)
			}
		}
	}
	sort.Strings(inUse)
	sort.Strings(leaving)
	return inUse, leaving, nil
}

// veleroBackupUnrestorable returns why the named Velero Backup cannot be
// restored, or "" when it can. Only a Completed or a PartiallyFailed Backup
// holds data to restore; Velero itself validates only that the Backup exists
// and its storage location is usable, so a Restore from one in another phase
// would take the target apart for nothing.
func (r *RestoreJobReconciler) veleroBackupUnrestorable(ctx context.Context, name string) (string, error) {
	vb := &velerov1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: veleroNamespace, Name: name}, vb); err != nil {
		if errors.IsNotFound(err) {
			return fmt.Sprintf("Velero Backup %s/%s no longer exists (expired or deleted)", veleroNamespace, name), nil
		}
		return "", err
	}
	switch vb.Status.Phase {
	case velerov1.BackupPhaseCompleted, velerov1.BackupPhasePartiallyFailed:
		return "", nil
	}
	return fmt.Sprintf("Velero Backup %s/%s is %q and cannot be restored", veleroNamespace, name, vb.Status.Phase), nil
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

// getOrigPVC returns the PVC named name, or nil when there is none. It reads
// past the cache: a retried rename must see the -orig PVC it created, not
// take the name for free and fail to create it again.
func (r *RestoreJobReconciler) getOrigPVC(ctx context.Context, ns, name string) (*corev1.PersistentVolumeClaim, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.apiReader().Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, pvc); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return pvc, nil
}

// foreignOrigPVC fails when pvc exists and restoreJob did not create it.
// Proceeding would hand the live disk to the restore under its own name, as
// keepOriginalPVC=false does, while the -orig name holds some other disk.
func foreignOrigPVC(pvc *corev1.PersistentVolumeClaim, restoreJob *backupsv1alpha1.RestoreJob) error {
	if pvc == nil || pvc.Annotations[origPVCRestoreJobUIDAnnotation] == string(restoreJob.UID) {
		return nil
	}
	return fmt.Errorf("PVC %s/%s already exists and was not created by this RestoreJob, so the current disk cannot be kept under that name: "+
		"remove that PVC or create the RestoreJob under another name", pvc.Namespace, pvc.Name)
}

// renamePVC preserves an existing PVC by rebinding it under a new name.
// The original PVC is deleted and a new one pointing to the same PV is created.
// Missing resources are silently skipped (non-fatal). Each step can be
// resumed, so a rename interrupted by a controller restart completes on the
// next reconcile.
func (r *RestoreJobReconciler) renamePVC(ctx context.Context, restoreJob *backupsv1alpha1.RestoreJob, ns, oldName, newName string) error {
	logger := getLogger(ctx)

	newPVC, err := r.getOrigPVC(ctx, ns, newName)
	if err != nil {
		return err
	}
	if err := foreignOrigPVC(newPVC, restoreJob); err != nil {
		return err
	}
	resuming := newPVC != nil

	oldPVC := &corev1.PersistentVolumeClaim{}
	if err := r.apiReader().Get(ctx, client.ObjectKey{Namespace: ns, Name: oldName}, oldPVC); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		oldPVC = nil
	}

	pv := &corev1.PersistentVolume{}
	var pvName string
	switch {
	case resuming:
		pvName = newPVC.Spec.VolumeName
	case oldPVC == nil:
		return nil // nothing to rename
	case oldPVC.Spec.VolumeName == "":
		logger.Debug("PVC not bound, deleting", "name", oldName)
		return r.Delete(ctx, oldPVC)
	default:
		pvName = oldPVC.Spec.VolumeName

		// Patch PV reclaim policy to Retain so it survives PVC deletion.
		// A policy already recorded is the one from before any rename.
		if err := r.apiReader().Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
			return fmt.Errorf("failed to get PV %s: %w", pvName, err)
		}
		originalPolicy, recorded := pv.Annotations[originalReclaimPolicyAnnotation]
		if !recorded {
			originalPolicy = string(pv.Spec.PersistentVolumeReclaimPolicy)
			metav1.SetMetaDataAnnotation(&pv.ObjectMeta, originalReclaimPolicyAnnotation, originalPolicy)
		}
		if !recorded || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
			pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
			if err := r.Update(ctx, pv); err != nil {
				return fmt.Errorf("failed to set Retain policy on PV %s: %w", pvName, err)
			}
		}

		// Create the new -orig PVC first (unbound, just the object)
		newPVC = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      newName,
				Namespace: ns,
				Annotations: map[string]string{
					originalReclaimPolicyAnnotation: originalPolicy,
					origPVCRestoreJobUIDAnnotation:  string(restoreJob.UID),
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      oldPVC.Spec.AccessModes,
				Resources:        oldPVC.Spec.Resources,
				StorageClassName: oldPVC.Spec.StorageClassName,
				VolumeMode:       oldPVC.Spec.VolumeMode,
				VolumeName:       pvName,
			},
		}
		// Create fills in the UID the claimRef needs. Reading the PVC back
		// through the cache can miss it, as it is this reconcile's own write.
		if err := r.Create(ctx, newPVC); err != nil {
			return fmt.Errorf("failed to create -orig PVC %s: %w", newName, err)
		}
	}

	// Delete the original PVC. Only while it still claims the kept PV: after
	// an interrupted rename, CDI may have recreated an empty one under the old
	// name, and that one is left to the restore.
	if oldPVC != nil && oldPVC.Spec.VolumeName == pvName {
		if err := r.Delete(ctx, oldPVC); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete PVC %s: %w", oldName, err)
		}
	}

	// Point the PV's claimRef directly to the new -orig PVC.
	// This is atomic — no window where the PV is Available for other PVCs to grab.
	// The PV controller writes the PV too once the original PVC is gone, so
	// the read is uncached and a conflict is retried.
	rebound := false
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.apiReader().Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
			return err
		}
		if ref := pv.Spec.ClaimRef; ref != nil && ref.Namespace == ns && ref.Name == newName && ref.UID == newPVC.UID {
			return nil
		}
		pv.Spec.ClaimRef = &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
			Namespace:  ns,
			Name:       newName,
			UID:        newPVC.UID,
		}
		rebound = true
		return r.Update(ctx, pv)
	})
	if err != nil {
		return fmt.Errorf("failed to rebind PV %s to %s: %w", pvName, newName, err)
	}
	if !rebound {
		return nil
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
	templateContext := map[string]any{
		"Application": map[string]any{
			"metadata": map[string]any{
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
	if vmRes := getVMInstanceResources(ur); vmRes != nil && backup.Spec.ApplicationRef.Kind == vmInstanceKind {
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
			APIGroup: new(""),
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
