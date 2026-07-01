// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines strategy.backups.cozystack.io API types.
//
// Group: strategy.backups.cozystack.io
// Version: v1alpha1
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion,
			&FoundationDB{},
			&FoundationDBList{},
		)
		return nil
	})
}

const (
	FoundationDBStrategyKind = "FoundationDB"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// FoundationDB defines a backup strategy that delegates execution to the
// FoundationDB Kubernetes operator (apps.foundationdb.org). The strategy
// carries a templated FoundationDBBackup-like configuration; the driver
// materialises apps.foundationdb.org/v1beta2 FoundationDBBackup objects per
// Cozystack BackupJob and surfaces them as Cozystack Backup artefacts.
// Restores are driven by materialising FoundationDBRestore CRs against the
// target FoundationDBCluster (in-place: same cluster; to-copy: a freshly
// deployed cluster).
type FoundationDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FoundationDBSpec   `json:"spec,omitempty"`
	Status FoundationDBStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FoundationDBList contains a list of FoundationDB backup strategies.
type FoundationDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FoundationDB `json:"items"`
}

// FoundationDBSpec specifies the desired FoundationDB-operator-driven backup
// strategy.
type FoundationDBSpec struct {
	// Template carries the templated FoundationDBBackup/FoundationDBRestore
	// configuration applied per BackupJob and RestoreJob. String fields
	// support Helm-style Go templating with two top-level values:
	//   .Application - the application object (apps.cozystack.io/FoundationDB)
	//   .Parameters  - the parameters from the matched BackupClassStrategy.
	//                  These values MUST NOT carry credentials. Route S3
	//                  access keys through BackupDeploymentPodTemplateSpec
	//                  volumes/env (typical pattern: mount a Secret-backed
	//                  blob_credentials.json file and set
	//                  FDB_BLOB_CREDENTIALS to its path, or pass
	//                  --blob_credentials=<path> via CustomParameters).
	Template FoundationDBTemplate `json:"template"`
}

// FoundationDBTemplate describes the templated FoundationDBBackup-shaped
// configuration the driver renders per BackupJob (and the matching
// FoundationDBRestore on the restore path).
type FoundationDBTemplate struct {
	// BlobStoreConfiguration is the templated blob-store configuration
	// passed verbatim to the FDB operator (apps.foundationdb.org). Field
	// semantics mirror apps.foundationdb.org/v1beta2 BlobStoreConfiguration.
	BlobStoreConfiguration FoundationDBBlobStoreTemplate `json:"blobStoreConfiguration"`

	// AgentCount is the number of backup_agent pods the operator runs in
	// the backup deployment. When zero, the operator default applies.
	// +optional
	AgentCount *int32 `json:"agentCount,omitempty"`

	// SnapshotPeriodSeconds controls how often the backup_agent rotates a
	// full snapshot inside the backup directory. When zero, the operator
	// default applies (3600).
	// +optional
	SnapshotPeriodSeconds *int32 `json:"snapshotPeriodSeconds,omitempty"`

	// CustomParameters are extra knobs passed to the backup_agent and
	// fdbrestore processes. Templating is supported on each element.
	// Common entries include --blob_credentials=<path> and the
	// --knob_http_request_* tuning knobs.
	// +optional
	CustomParameters []string `json:"customParameters,omitempty"`

	// EncryptionKeyPath is the in-container path to a backup encryption
	// key file. Templating is supported.
	// +optional
	EncryptionKeyPath string `json:"encryptionKeyPath,omitempty"`

	// BackupDeploymentPodTemplateSpec is the PodTemplateSpec the driver
	// stamps onto FoundationDBBackup.spec.backupDeploymentSpec.podTemplateSpec.
	// Admins use it to mount credentials, CA bundles, or any other
	// per-tenant secrets the backup_agent needs. Templating is supported
	// on every string field.
	// +optional
	BackupDeploymentPodTemplateSpec *corev1.PodTemplateSpec `json:"backupDeploymentPodTemplateSpec,omitempty"`
}

// FoundationDBBlobStoreTemplate is a typed, kubebuilder-validated mirror of
// apps.foundationdb.org/v1beta2 BlobStoreConfiguration. Restores reuse the
// same shape: the driver re-renders the strategy and points the
// FoundationDBRestore at the BackupName recorded on the source Backup
// artefact.
type FoundationDBBlobStoreTemplate struct {
	// AccountName is the operator's blob account identifier
	// (typically "<key>@<endpoint-host>"). Templating is supported.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	AccountName string `json:"accountName"`

	// Bucket is the S3 (or compatible) bucket name. Templating is
	// supported. When empty the operator uses the BackupName as the
	// path root only.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Bucket string `json:"bucket,omitempty"`

	// BackupName is the per-cluster path segment under the bucket. When
	// empty, the driver fills it per BackupJob to give each run a
	// discrete S3 path (one operator backup directory per Cozystack
	// Backup artefact). Templating is supported.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	BackupName string `json:"backupName,omitempty"`

	// URLParameters are extra blob-store URL parameters passed through
	// to the operator (e.g. "secure_connection=0", "region=us-east-1").
	// Templating is supported on each element.
	// +optional
	URLParameters []string `json:"urlParameters,omitempty"`
}

// FoundationDBStatus reports observed state for the strategy CR. Driver
// controllers surface diagnostic conditions here (e.g. validation issues).
type FoundationDBStatus struct {
	// Conditions holds the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
