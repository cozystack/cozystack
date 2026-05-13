// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines strategy.backups.cozystack.io API types.
//
// Group: strategy.backups.cozystack.io
// Version: v1alpha1
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion,
			&Etcd{},
			&EtcdList{},
		)
		return nil
	})
}

const (
	EtcdStrategyKind = "Etcd"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Etcd defines a backup strategy that delegates execution to the
// etcd-operator (etcd.aenix.io). The strategy carries a templated backup
// destination configuration; the driver materialises one
// etcd.aenix.io/v1alpha1 EtcdBackup per Cozystack BackupJob and surfaces
// it as a Cozystack Backup artefact. Restores are driven by stamping the
// rendered destination back onto EtcdCluster.spec.bootstrap.restore.source
// on the target Etcd application - either by recreating the source
// EtcdCluster (in-place) or by patching a freshly provisioned target
// EtcdCluster before the operator marks it Ready (to-copy).
type Etcd struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EtcdSpec   `json:"spec,omitempty"`
	Status EtcdStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EtcdList contains a list of Etcd backup strategies.
type EtcdList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Etcd `json:"items"`
}

// EtcdSpec specifies the desired etcd-operator-driven backup strategy.
type EtcdSpec struct {
	// Template carries the templated EtcdBackup destination configuration
	// applied per BackupJob (and re-rendered against the same
	// .Application / .Parameters at restore time). String fields support
	// Helm-style Go templating with two top-level values:
	//   .Application - the application object (apps.cozystack.io/Etcd)
	//   .Parameters  - the parameters from the matched BackupClassStrategy.
	//                  These values MUST NOT carry credentials; route S3
	//                  access keys through
	//                  Destination.S3.CredentialsSecretRef.
	Template EtcdTemplate `json:"template"`
}

// EtcdTemplate describes the templated EtcdBackup-shaped configuration the
// driver renders per BackupJob (and on the restore path stamps onto
// EtcdCluster.spec.bootstrap.restore.source).
type EtcdTemplate struct {
	// Destination defines where the etcd snapshot will be stored. Field
	// semantics mirror etcd.aenix.io/v1alpha1 EtcdBackup.spec.destination.
	Destination EtcdDestinationTemplate `json:"destination"`
}

// EtcdDestinationTemplate mirrors etcd.aenix.io/v1alpha1
// EtcdBackup.spec.destination. Exactly one of S3 or PVC must be set; the
// XValidation rule rejects ambiguous templates at admission time so the
// driver never has to invent precedence.
// +kubebuilder:validation:XValidation:rule="(has(self.s3) ? 1 : 0) + (has(self.pvc) ? 1 : 0) == 1",message="exactly one of s3 or pvc must be set"
type EtcdDestinationTemplate struct {
	// S3 configures an S3-compatible storage target. Templating is
	// supported on every string field (Bucket, Endpoint, Key, Region,
	// CredentialsSecretRef.Name).
	// +optional
	S3 *EtcdS3Template `json:"s3,omitempty"`

	// PVC configures a PersistentVolumeClaim-backed storage target.
	// Templating is supported on ClaimName and SubPath.
	// +optional
	PVC *EtcdPVCTemplate `json:"pvc,omitempty"`
}

// EtcdS3Template is a typed, kubebuilder-validated mirror of
// etcd.aenix.io/v1alpha1 EtcdBackup.spec.destination.s3. Restores reuse
// the same shape: the driver re-renders the strategy and stamps the
// resulting block onto EtcdCluster.spec.bootstrap.restore.source.s3 of the
// target Etcd application.
type EtcdS3Template struct {
	// Bucket is the S3 (or compatible) bucket name. Templating is
	// supported.
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// Endpoint is the S3-compatible endpoint URL, including scheme (e.g.
	// "https://s3.amazonaws.com" or
	// "http://seaweedfs-s3.tenant-root.svc:8333"). Templating is
	// supported.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// Key is the key prefix (directory path) within the bucket. The
	// etcd-operator appends the snapshot filename automatically.
	// Templating is supported.
	// +optional
	Key string `json:"key,omitempty"`

	// Region is the AWS region for the S3 bucket. Templating is
	// supported.
	// +optional
	Region string `json:"region,omitempty"`

	// ForcePathStyle forces path-style S3 URLs (e.g., endpoint/bucket/key)
	// instead of virtual-hosted-style (e.g., bucket.endpoint/key). Most
	// S3-compatible providers (MinIO, Ceph, seaweedfs-s3) require path
	// style.
	// +optional
	ForcePathStyle *bool `json:"forcePathStyle,omitempty"`

	// CredentialsSecretRef references a Secret containing
	// AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY keys. The Secret must
	// live in the application's namespace. Templating is supported on
	// Name (typical pattern:
	// "{{ .Application.metadata.name }}-etcd-backup-creds").
	CredentialsSecretRef EtcdLocalObjectReference `json:"credentialsSecretRef"`
}

// EtcdLocalObjectReference is a minimal local Secret reference. The driver
// looks the Secret up in the application namespace.
type EtcdLocalObjectReference struct {
	// Name is the Secret name. Templating is supported.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// EtcdPVCTemplate mirrors etcd.aenix.io/v1alpha1
// EtcdBackup.spec.destination.pvc. The driver stamps the rendered fields
// onto the operator-side EtcdBackup verbatim; the operator mounts the PVC
// and appends the snapshot filename under SubPath.
type EtcdPVCTemplate struct {
	// ClaimName is the name of the PersistentVolumeClaim to use as the
	// backup destination. The PVC must live in the application's
	// namespace and be ReadWriteMany if multiple BackupJobs may run
	// concurrently. Templating is supported.
	// +kubebuilder:validation:MinLength=1
	ClaimName string `json:"claimName"`

	// SubPath is an optional sub-directory within the PVC volume. The
	// operator appends the snapshot filename automatically. Templating is
	// supported.
	// +optional
	SubPath string `json:"subPath,omitempty"`
}

// EtcdStatus reports observed state for the strategy CR. Driver
// controllers surface diagnostic conditions here (e.g. validation issues).
type EtcdStatus struct {
	// Conditions holds the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
