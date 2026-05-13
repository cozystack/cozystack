// SPDX-License-Identifier: Apache-2.0

// Package foundationdbtypes declares the minimum subset of the
// apps.foundationdb.org/v1beta2 CRD shape that the backupstrategy controller
// operates on. We avoid pulling the full upstream fdb-kubernetes-operator Go
// API (which transitively imports a large surface area we do not need) while
// letting us drop unstructured.Unstructured from the controller and tests.
//
// MergeFrom-style patches preserve unknown fields. Get + Patch builds two
// values from the same partial schema; the JSON merge patch is computed
// between them, so it only contains the fields we know about. The API
// server applies that patch to the full stored object on the server side,
// leaving everything else untouched.
//
// +groupName=apps.foundationdb.org
// +versionName=v1beta2
package foundationdbtypes

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "apps.foundationdb.org"
	Version   = "v1beta2"

	// BackupStateRunning / Stopped mirror the operator's
	// FoundationDBBackup.spec.backupState enum the driver actually
	// writes. Flipping a Running backup to Stopped closes its
	// blob-store directory; the operator allows a new backup to start
	// under a different BackupName.
	//
	// Note: the operator-side enum also defines "Paused", but the
	// driver does not read or write it. It is deliberately omitted
	// here so a future code path that needs to pause/resume backups
	// has to add the constant alongside the reader/writer, not import
	// a dead identifier.
	BackupStateRunning = "Running"
	BackupStateStopped = "Stopped"

	// RestoreStateCompleted is the operator-side terminal success
	// marker on FoundationDBRestore.status.state. The operator emits
	// the literal string "Completed" once fdbrestore finishes.
	RestoreStateCompleted = "Completed"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&FoundationDBCluster{}, &FoundationDBClusterList{},
		&FoundationDBBackup{}, &FoundationDBBackupList{},
		&FoundationDBRestore{}, &FoundationDBRestoreList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// ---------------------------------------------------------------------------
// FoundationDBCluster (existence check only - the driver never patches this)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type FoundationDBCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FoundationDBClusterSpec   `json:"spec,omitempty"`
	Status            FoundationDBClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FoundationDBClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FoundationDBCluster `json:"items"`
}

// FoundationDBClusterSpec is intentionally minimal: the driver only needs
// to confirm the cluster exists before issuing a backup/restore against it.
type FoundationDBClusterSpec struct {
	Version string `json:"version,omitempty"`
}

// FoundationDBClusterStatus mirrors the few status fields the driver uses
// as readiness gates.
type FoundationDBClusterStatus struct {
	// Health.Available is the boolean the chart's e2e wait gates on; the
	// driver reuses it to defer materialising a FoundationDBBackup until
	// the cluster has finished bootstrapping.
	Health FoundationDBClusterHealth `json:"health,omitempty"`
}

type FoundationDBClusterHealth struct {
	Available bool `json:"available,omitempty"`
}

// ---------------------------------------------------------------------------
// FoundationDBBackup (continuous backup_agent deployment)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type FoundationDBBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FoundationDBBackupSpec   `json:"spec,omitempty"`
	Status            FoundationDBBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FoundationDBBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FoundationDBBackup `json:"items"`
}

// FoundationDBBackupSpec mirrors the subset of
// apps.foundationdb.org/v1beta2 FoundationDBBackup.spec the driver writes.
// Unknown fields are preserved by the server during merge patches;
// everything we don't set falls back to operator defaults.
type FoundationDBBackupSpec struct {
	// ClusterName is the FoundationDBCluster the backup_agent attaches to.
	ClusterName string `json:"clusterName"`

	// Version is the FDB version the backup_agent runs (must match the
	// source cluster). Required by the operator's OpenAPI schema; the
	// driver reads it from FoundationDBCluster.spec.version.
	Version string `json:"version"`

	// BackupState is one of "Running", "Stopped", "Paused". The driver
	// flips prior backups to Stopped before creating a new one so each
	// Cozystack BackupJob owns a discrete blob-store directory.
	BackupState string `json:"backupState,omitempty"`

	// BlobStoreConfiguration carries the blob-store target.
	BlobStoreConfiguration BlobStoreConfiguration `json:"blobStoreConfiguration,omitempty"`

	// AgentCount sets the backup_agent deployment replica count.
	// +optional
	AgentCount *int32 `json:"agentCount,omitempty"`

	// SnapshotPeriodSeconds rotates a full snapshot inside the backup
	// directory at this cadence. When zero, the operator default applies.
	// +optional
	SnapshotPeriodSeconds *int32 `json:"snapshotPeriodSeconds,omitempty"`

	// CustomParameters carries extra knobs passed to the backup_agent.
	// +optional
	CustomParameters []string `json:"customParameters,omitempty"`

	// EncryptionKeyPath is the in-container path to a backup encryption
	// key file.
	// +optional
	EncryptionKeyPath string `json:"encryptionKeyPath,omitempty"`

	// BackupDeploymentSpec stamps custom pod-template settings onto the
	// operator-managed backup_agent Deployment (credentials volumes,
	// resource overrides, etc.). Only PodTemplateSpec is modelled — other
	// fields on the operator-side type are preserved on the server.
	// +optional
	BackupDeploymentSpec *BackupDeploymentSpec `json:"backupDeploymentSpec,omitempty"`
}

// BackupDeploymentSpec mirrors apps.foundationdb.org/v1beta2
// FoundationDBBackup.spec.backupDeploymentSpec. The driver only writes
// PodTemplateSpec today; other sub-fields stay on the server side.
type BackupDeploymentSpec struct {
	// PodTemplateSpec is the PodTemplateSpec the operator stamps onto
	// the backup_agent Deployment.
	// +optional
	PodTemplateSpec *corev1.PodTemplateSpec `json:"podTemplateSpec,omitempty"`
}

// FoundationDBBackupStatus mirrors the subset of
// apps.foundationdb.org/v1beta2 FoundationDBBackup.status the driver reads
// to decide when an artefact is ready.
type FoundationDBBackupStatus struct {
	AgentCount           int32                  `json:"agentCount,omitempty"`
	DeploymentConfigured bool                   `json:"deploymentConfigured,omitempty"`
	BackupDetails        *BackupDetails         `json:"backupDetails,omitempty"`
	Generations          *BackupGenerationStatus `json:"generations,omitempty"`
}

// BackupDetails mirrors apps.foundationdb.org/v1beta2
// FoundationDBBackupStatus.backupDetails. SnapshotTime > 0 means a full
// snapshot has been written and the backup is restorable.
type BackupDetails struct {
	Paused       bool   `json:"paused,omitempty"`
	Running      bool   `json:"running,omitempty"`
	SnapshotTime int64  `json:"snapshotTime,omitempty"`
	URL          string `json:"url,omitempty"`
}

// BackupGenerationStatus mirrors
// apps.foundationdb.org/v1beta2 FoundationDBBackupStatus.generations. The
// driver reads Reconciled to confirm the operator has observed the latest
// spec generation before declaring a backup ready.
type BackupGenerationStatus struct {
	Reconciled                int64 `json:"reconciled,omitempty"`
	NeedsBackupStart          int64 `json:"needsBackupStart,omitempty"`
	NeedsBackupStop           int64 `json:"needsBackupStop,omitempty"`
	NeedsBackupPauseToggle    int64 `json:"needsBackupPauseToggle,omitempty"`
	NeedsBackupAgentUpdate    int64 `json:"needsBackupAgentUpdate,omitempty"`
	NeedsBackupModification   int64 `json:"needsBackupModification,omitempty"`
}

// BlobStoreConfiguration mirrors apps.foundationdb.org/v1beta2
// BlobStoreConfiguration verbatim. The operator treats AccountName as the
// blob-store routing key ("<key>@<endpoint-host>"); credentials are
// resolved from a file referenced by --blob_credentials= on the
// backup_agent and fdbrestore command lines.
type BlobStoreConfiguration struct {
	AccountName   string   `json:"accountName"`
	BackupName    string   `json:"backupName,omitempty"`
	Bucket        string   `json:"bucket,omitempty"`
	URLParameters []string `json:"urlParameters,omitempty"`
}

// ---------------------------------------------------------------------------
// FoundationDBRestore (one-shot fdbrestore)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type FoundationDBRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FoundationDBRestoreSpec   `json:"spec,omitempty"`
	Status            FoundationDBRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FoundationDBRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FoundationDBRestore `json:"items"`
}

// FoundationDBRestoreSpec mirrors the subset of
// apps.foundationdb.org/v1beta2 FoundationDBRestore.spec the driver writes.
type FoundationDBRestoreSpec struct {
	// DestinationClusterName is the FoundationDBCluster the restored
	// keyspace is rebuilt into.
	DestinationClusterName string `json:"destinationClusterName"`

	// BlobStoreConfiguration carries the source backup directory.
	BlobStoreConfiguration BlobStoreConfiguration `json:"blobStoreConfiguration,omitempty"`

	// CustomParameters carries extra knobs passed to fdbrestore.
	// +optional
	CustomParameters []string `json:"customParameters,omitempty"`

	// EncryptionKeyPath is the in-container path to a backup encryption
	// key file.
	// +optional
	EncryptionKeyPath string `json:"encryptionKeyPath,omitempty"`

	// KeyRanges scopes the restore to a subset of the keyspace. Empty
	// means restore everything.
	// +optional
	KeyRanges []KeyRange `json:"keyRanges,omitempty"`
}

// KeyRange mirrors apps.foundationdb.org/v1beta2
// FoundationDBRestoreSpec.keyRanges entry.
type KeyRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// FoundationDBRestoreStatus mirrors
// apps.foundationdb.org/v1beta2 FoundationDBRestore.status.
type FoundationDBRestoreStatus struct {
	Running bool   `json:"running,omitempty"`
	State   string `json:"state,omitempty"`
}
