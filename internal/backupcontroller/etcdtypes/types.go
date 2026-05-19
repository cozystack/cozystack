// SPDX-License-Identifier: Apache-2.0

// Package etcdtypes declares the minimum subset of the
// etcd.aenix.io/v1alpha1 CRD shape that the backupstrategy controller
// operates on. We avoid pulling the full upstream etcd-operator Go API
// (which transitively imports a large surface area we do not need) while
// letting us drop unstructured.Unstructured from the controller and
// tests.
//
// MergeFrom-style patches preserve unknown fields. Get + Patch builds two
// values from the same partial schema; the JSON merge patch is computed
// between them, so it only contains the fields we know about. The API
// server applies that patch to the full stored object on the server side,
// leaving everything else untouched.
//
// +groupName=etcd.aenix.io
// +versionName=v1alpha1
package etcdtypes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "etcd.aenix.io"
	Version   = "v1alpha1"

	// BackupPhase* mirror etcd.aenix.io/v1alpha1
	// EtcdBackup.status.phase. The upstream controller emits these
	// literal strings; the driver reads them to decide when a
	// BackupJob is done.
	//
	// Pending: operator has not started the Job yet.
	// Started: Job is running; snapshot upload is in progress.
	// Complete: snapshot landed in S3/PVC; the Cozystack Backup can be
	//   materialised.
	// Failed: terminal failure; the BackupJob is marked Failed and the
	//   driver surfaces the operator-side message on the condition.
	BackupPhasePending  = "Pending"
	BackupPhaseStarted  = "Started"
	BackupPhaseComplete = "Complete"
	BackupPhaseFailed   = "Failed"

	// ClusterConditionReady is the condition type the etcd-operator
	// raises once an EtcdCluster has finished bootstrapping (every
	// member elected, peer/client TLS materialised). The driver gates
	// restore success on it.
	ClusterConditionReady = "Ready"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&EtcdCluster{}, &EtcdClusterList{},
		&EtcdBackup{}, &EtcdBackupList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// ---------------------------------------------------------------------------
// EtcdCluster
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type EtcdCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EtcdClusterSpec   `json:"spec,omitempty"`
	Status            EtcdClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type EtcdClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EtcdCluster `json:"items"`
}

// EtcdClusterSpec mirrors the subset of etcd.aenix.io/v1alpha1
// EtcdCluster.spec the driver writes during restore. Unknown fields are
// preserved on the server side; everything we don't set falls back to
// chart/operator defaults.
type EtcdClusterSpec struct {
	// Bootstrap configures initial-cluster initialization from an existing
	// data source. The operator only consults this on first reconcile of
	// the EtcdCluster - mutating it after the cluster is Ready has no
	// effect. The driver therefore patches it before the operator
	// observes the new EtcdCluster (in-place: delete + recreate; to-copy:
	// patch a freshly-rendered EtcdCluster before it goes Ready).
	// +optional
	Bootstrap *EtcdClusterBootstrap `json:"bootstrap,omitempty"`
}

// EtcdClusterBootstrap mirrors EtcdCluster.spec.bootstrap.
type EtcdClusterBootstrap struct {
	// Restore configures bootstrap from an existing snapshot.
	// +optional
	Restore *EtcdClusterRestore `json:"restore,omitempty"`
}

// EtcdClusterRestore mirrors EtcdCluster.spec.bootstrap.restore.
type EtcdClusterRestore struct {
	// Source is the snapshot location.
	Source EtcdBackupDestination `json:"source"`
}

// EtcdClusterStatus mirrors the subset of EtcdCluster.status the driver
// reads as a readiness gate.
type EtcdClusterStatus struct {
	// Conditions reflects the latest observations. The driver checks the
	// "Ready" entry.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ---------------------------------------------------------------------------
// EtcdBackup (one-shot snapshot Job)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type EtcdBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EtcdBackupSpec   `json:"spec,omitempty"`
	Status            EtcdBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type EtcdBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EtcdBackup `json:"items"`
}

// EtcdBackupSpec mirrors etcd.aenix.io/v1alpha1 EtcdBackup.spec. The
// driver materialises one of these per Cozystack BackupJob so each
// snapshot lives in a deterministic location keyed off the BackupJob
// name; the operator handles the Job and writes back status.phase.
type EtcdBackupSpec struct {
	// ClusterRef references the EtcdCluster to snapshot.
	ClusterRef EtcdLocalObjectReference `json:"clusterRef"`

	// Destination is where the snapshot will be uploaded.
	Destination EtcdBackupDestination `json:"destination"`
}

// EtcdBackupStatus mirrors etcd.aenix.io/v1alpha1 EtcdBackup.status. The
// driver reads Phase to decide when the snapshot is done, and Snapshot
// to populate the cozystack Backup artefact pass-through.
type EtcdBackupStatus struct {
	// Phase is one of Pending / Started / Complete / Failed.
	Phase string `json:"phase,omitempty"`

	// Conditions reflects per-aspect state (e.g. the upload condition).
	// The driver surfaces the latest message back onto the Cozystack
	// BackupJob on failure.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Snapshot is the URI/size/checksum trio populated by the
	// etcd-operator's EtcdBackup controller after the backup Job
	// reaches phase=Complete. Restore-friendly contract: the URI is
	// the FULL S3 key (or file path) including rev/timestamp suffix
	// the backup-agent injects at write time, which the spec
	// destination alone doesn't carry.
	//
	// Available from etcd-operator v0.4.4 onward (upstream PR
	// aenix-io/etcd-operator#316). The bundled chart at
	// packages/system/etcd-operator pins v0.4.4+, so the driver can
	// rely on this field being populated on every Complete EtcdBackup.
	// The defensive nil-check in the pass-through path stays to
	// handle pre-Complete reconciles and a forensic downgrade.
	// +optional
	Snapshot *EtcdBackupSnapshot `json:"snapshot,omitempty"`
}

// EtcdBackupSnapshot mirrors etcd.aenix.io/v1alpha1
// BackupSnapshot. Field tags match upstream
// (api/v1alpha1/etcdbackup_types.go) including the required URI; see
// internal/controller/etcdbackup_controller.go in aenix-io/etcd-operator
// for the marker-scan parser that populates this struct.
type EtcdBackupSnapshot struct {
	// URI is the canonical location of the snapshot, e.g.
	// "s3://<bucket>/<key>" or "file://<abs-path>". Required upstream
	// (no omitempty) so the schema rejects empty values; we mirror
	// the omission for consistency with the source-of-truth shape.
	URI string `json:"uri"`

	// SizeBytes is the snapshot size as observed by the agent at
	// write time. Zero only when the upstream agent's emit overflows
	// int64 — that branch deliberately keeps the URI/Checksum landed
	// so a reviewer sees the snapshot exists.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Checksum is "<algo>:<hex>" of the snapshot bytes. Currently
	// always "sha256:<hex>" when set; consumers MUST tolerate other
	// algorithms via the prefix (upstream admits sha3-256, blake2b-256,
	// blake3-256 by pattern).
	// +optional
	Checksum string `json:"checksum,omitempty"`
}

// ---------------------------------------------------------------------------
// Shared types (destination shape is identical for EtcdBackup.spec and
// EtcdCluster.spec.bootstrap.restore.source)
// ---------------------------------------------------------------------------

// EtcdBackupDestination mirrors etcd.aenix.io/v1alpha1
// EtcdBackup.spec.destination AND
// EtcdCluster.spec.bootstrap.restore.source. The upstream CRDs share the
// same shape; one Go type lets the driver render the same destination
// onto both code paths.
type EtcdBackupDestination struct {
	// S3 configures S3-compatible storage. Exactly one of S3 or PVC.
	// +optional
	S3 *EtcdBackupS3 `json:"s3,omitempty"`

	// PVC configures PersistentVolumeClaim-backed storage.
	// +optional
	PVC *EtcdBackupPVC `json:"pvc,omitempty"`
}

// EtcdBackupS3 mirrors etcd.aenix.io/v1alpha1 EtcdBackup.spec.destination.s3.
type EtcdBackupS3 struct {
	Bucket               string                   `json:"bucket"`
	Endpoint             string                   `json:"endpoint"`
	Key                  string                   `json:"key,omitempty"`
	Region               string                   `json:"region,omitempty"`
	ForcePathStyle       *bool                    `json:"forcePathStyle,omitempty"`
	CredentialsSecretRef EtcdLocalObjectReference `json:"credentialsSecretRef"`
}

// EtcdBackupPVC mirrors etcd.aenix.io/v1alpha1
// EtcdBackup.spec.destination.pvc.
type EtcdBackupPVC struct {
	ClaimName string `json:"claimName"`
	SubPath   string `json:"subPath,omitempty"`
}

// EtcdLocalObjectReference is a minimal local Secret/EtcdCluster
// reference. Mirrors the upstream operator's bare {name: ""} shape.
type EtcdLocalObjectReference struct {
	Name string `json:"name"`
}
