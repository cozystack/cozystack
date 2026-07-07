// SPDX-License-Identifier: Apache-2.0

// Package etcdtypes declares the minimum subset of the
// etcd-operator.cozystack.io/v1alpha2 CRD shape that the backupstrategy
// controller operates on. We avoid pulling the full upstream etcd-operator
// Go API (which transitively imports a large surface area we do not need)
// while letting us drop unstructured.Unstructured from the controller and
// tests.
//
// MergeFrom-style patches preserve unknown fields. Get + Patch builds two
// values from the same partial schema; the JSON merge patch is computed
// between them, so it only contains the fields we know about. The API
// server applies that patch to the full stored object on the server side,
// leaving everything else untouched.
//
// +groupName=etcd-operator.cozystack.io
// +versionName=v1alpha2
package etcdtypes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "etcd-operator.cozystack.io"
	Version   = "v1alpha2"

	// BackupPhase* mirror etcd-operator.cozystack.io/v1alpha2
	// EtcdSnapshot.status.phase. The upstream controller emits these
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

	// ClusterConditionAvailable is the condition type the etcd-operator
	// raises once an EtcdCluster has a healthy quorum (every member
	// elected, peer/client TLS materialised). The driver gates backup and
	// restore readiness on it. The v1alpha2 operator reports
	// Available/Progressing/Degraded — there is no "Ready" condition.
	ClusterConditionAvailable = "Available"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&EtcdCluster{}, &EtcdClusterList{},
		&EtcdSnapshot{}, &EtcdSnapshotList{},
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

// EtcdClusterSpec mirrors the subset of etcd-operator.cozystack.io/v1alpha2
// EtcdCluster.spec the driver writes during restore. Unknown fields are
// preserved on the server side; everything we don't set falls back to
// chart/operator defaults.
type EtcdClusterSpec struct {
	// Bootstrap configures initial-cluster initialization from an existing
	// data source. The operator only consults this on first reconcile of
	// the EtcdCluster - mutating it after the cluster is Available has no
	// effect. The driver therefore stamps it onto the freshly-created
	// EtcdCluster during the in-place restore flow (suspend HR, snapshot
	// live spec, delete + recreate with bootstrap.restore.source set).
	// To-copy restore is not supported by this driver (see the Etcd
	// strategy type doc for details).
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
	// "Available" entry.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ---------------------------------------------------------------------------
// EtcdSnapshot (one-shot snapshot Job)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type EtcdSnapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EtcdSnapshotSpec   `json:"spec,omitempty"`
	Status            EtcdSnapshotStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type EtcdSnapshotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EtcdSnapshot `json:"items"`
}

// EtcdSnapshotSpec mirrors etcd-operator.cozystack.io/v1alpha2
// EtcdSnapshot.spec. The driver materialises one of these per Cozystack
// BackupJob so each snapshot lives in a deterministic location keyed off
// the BackupJob name; the operator handles the Job and writes back
// status.phase.
type EtcdSnapshotSpec struct {
	// ClusterRef references the EtcdCluster to snapshot. Upstream types it
	// as corev1.LocalObjectReference; the bare {name: ""} shape our mirror
	// emits is JSON-identical.
	ClusterRef EtcdLocalObjectReference `json:"clusterRef"`

	// Destination is where the snapshot will be uploaded.
	Destination EtcdBackupDestination `json:"destination"`
}

// EtcdSnapshotStatus mirrors etcd-operator.cozystack.io/v1alpha2
// EtcdSnapshot.status. The driver reads Phase to decide when the snapshot
// is done, and Artifact to populate the cozystack Backup artefact
// pass-through.
type EtcdSnapshotStatus struct {
	// Phase is one of Pending / Started / Complete / Failed.
	Phase string `json:"phase,omitempty"`

	// Conditions reflects per-aspect state (the "Ready" condition is True
	// only in the terminal Complete phase). The driver surfaces the latest
	// message back onto the Cozystack BackupJob on failure.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Artifact is the URI/size/checksum trio populated by the
	// etcd-operator's EtcdSnapshot controller after the snapshot Job
	// reaches phase=Complete. Restore-friendly contract: the URI is the
	// FULL S3 key (or file path) including the "<snapshot-name>.db" suffix
	// the operator appends at write time, which the spec destination alone
	// doesn't carry.
	//
	// The defensive nil-check in the pass-through path stays to handle
	// pre-Complete reconciles.
	// +optional
	Artifact *EtcdSnapshotArtifact `json:"artifact,omitempty"`
}

// EtcdSnapshotArtifact mirrors etcd-operator.cozystack.io/v1alpha2
// SnapshotArtifact (api/v1alpha2/etcdsnapshot_types.go).
type EtcdSnapshotArtifact struct {
	// URI is the canonical location of the snapshot, e.g.
	// "s3://<bucket>/<key>" or "file:///<abs-path>". Required upstream
	// (no omitempty) so the schema rejects empty values; we mirror the
	// omission for consistency with the source-of-truth shape.
	URI string `json:"uri"`

	// SizeBytes is the snapshot size as observed by the agent at write
	// time.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Checksum is "<algo>:<hex>" of the snapshot bytes. Currently always
	// "sha256:<hex>" when set; consumers MUST tolerate other algorithms
	// via the prefix.
	// +optional
	Checksum string `json:"checksum,omitempty"`
}

// ---------------------------------------------------------------------------
// Shared types (destination shape is identical for EtcdSnapshot.spec and
// EtcdCluster.spec.bootstrap.restore.source)
// ---------------------------------------------------------------------------

// EtcdBackupDestination mirrors etcd-operator.cozystack.io/v1alpha2
// SnapshotLocation (EtcdSnapshot.spec.destination) AND
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

// EtcdBackupS3 mirrors etcd-operator.cozystack.io/v1alpha2
// S3SnapshotLocation. ForcePathStyle is kept as *bool (rather than the
// upstream bool) so an unset value is omitted from the rendered JSON and
// the operator applies its own default.
type EtcdBackupS3 struct {
	Bucket               string                   `json:"bucket"`
	Endpoint             string                   `json:"endpoint"`
	Key                  string                   `json:"key,omitempty"`
	Region               string                   `json:"region,omitempty"`
	ForcePathStyle       *bool                    `json:"forcePathStyle,omitempty"`
	CredentialsSecretRef EtcdLocalObjectReference `json:"credentialsSecretRef"`
}

// EtcdBackupPVC mirrors etcd-operator.cozystack.io/v1alpha2
// PVCSnapshotLocation.
type EtcdBackupPVC struct {
	ClaimName string `json:"claimName"`
	SubPath   string `json:"subPath,omitempty"`
}

// EtcdLocalObjectReference is a minimal local Secret/EtcdCluster
// reference. JSON-identical to corev1.LocalObjectReference's {name: ""}
// shape.
type EtcdLocalObjectReference struct {
	Name string `json:"name"`
}
