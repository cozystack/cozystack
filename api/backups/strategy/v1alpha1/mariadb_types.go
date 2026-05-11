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
			&MariaDB{},
			&MariaDBList{},
		)
		return nil
	})
}

const (
	MariaDBStrategyKind = "MariaDB"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// MariaDB defines a backup strategy that delegates execution to the
// mariadb-operator (k8s.mariadb.com). The strategy carries a templated
// logical-Backup configuration; the driver materialises k8s.mariadb.com
// Backup objects per BackupJob and surfaces them as Cozystack Backup
// artifacts. Restores create a fresh k8s.mariadb.com/Restore CR with
// mariaDbRef pointing at the target MariaDB - the operator replays the
// logical dump into the live database; the driver does not touch
// MariaDB.spec.bootstrapFrom.
type MariaDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MariaDBSpec   `json:"spec,omitempty"`
	Status MariaDBStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MariaDBList contains a list of MariaDB backup strategies.
type MariaDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MariaDB `json:"items"`
}

// MariaDBSpec specifies the desired mariadb-operator-driven backup strategy.
type MariaDBSpec struct {
	// Template carries the templated k8s.mariadb.com/Backup configuration.
	// String fields support Helm-style Go templating with two top-level
	// values:
	//   .Application - the application object (apps.cozystack.io/MariaDB)
	//   .Parameters  - the parameters from the matched BackupClassStrategy.
	//                  These values MUST NOT carry credentials. Route S3
	//                  access keys through Storage.S3.AccessKeyIDSecretKeyRef
	//                  and Storage.S3.SecretAccessKeySecretKeyRef.
	Template MariaDBTemplate `json:"template"`
}

// MariaDBTemplate describes the templated k8s.mariadb.com/Backup shape the
// driver renders per BackupJob.
type MariaDBTemplate struct {
	// Storage defines the final storage for backups. Field semantics mirror
	// k8s.mariadb.com/v1alpha1 Backup.spec.storage. Exactly one of S3,
	// PersistentVolumeClaim or Volume must be set.
	Storage MariaDBStorageTemplate `json:"storage"`

	// Databases lists the logical databases to back up. When empty all
	// non-system databases are backed up. Templating is supported on each
	// element.
	// +optional
	Databases []string `json:"databases,omitempty"`

	// Compression algorithm used by mariadb-dump (e.g. "gzip", "bzip2",
	// "none"). Defaults to operator-side default when empty.
	// +optional
	Compression string `json:"compression,omitempty"`

	// MaxRetention is the maximum retention period for kept backups (e.g.
	// "720h" for 30 days). When empty, retention is unbounded and tenants
	// rely on Cozystack Plan-level retention only.
	// +optional
	MaxRetention *metav1.Duration `json:"maxRetention,omitempty"`

	// LogLevel controls log verbosity for mariadb-dump and the operator
	// Backup Job.
	// +optional
	LogLevel string `json:"logLevel,omitempty"`
}

// MariaDBStorageTemplate mirrors k8s.mariadb.com/v1alpha1 Backup.spec.storage.
// Exactly one of S3, PersistentVolumeClaim or Volume must be set.
type MariaDBStorageTemplate struct {
	// S3 configures an S3-compatible storage target. Templating is supported
	// on Bucket, Endpoint, Prefix, Region and Secret Name fields.
	// +optional
	S3 *MariaDBS3Template `json:"s3,omitempty"`

	// PersistentVolumeClaim configures a PVC-backed storage target. When set,
	// the driver creates a Kubernetes PVC that mariadb-operator mounts on the
	// Backup Job.
	// +optional
	PersistentVolumeClaim *corev1.PersistentVolumeClaimSpec `json:"persistentVolumeClaim,omitempty"`

	// Volume configures an arbitrary Kubernetes volume source as the backup
	// target.
	// +optional
	Volume *corev1.VolumeSource `json:"volume,omitempty"`
}

// MariaDBS3Template is a typed, kubebuilder-validated mirror of the
// k8s.mariadb.com/v1alpha1 Backup.spec.storage.s3 shape. The driver
// translates this struct into the operator-native Backup spec.
type MariaDBS3Template struct {
	// Bucket is the name of the S3 bucket that stores backups. Templating is
	// supported.
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// Endpoint is the S3 API endpoint without scheme (e.g.
	// "seaweedfs-s3.tenant-root.svc:8333"). Templating is supported.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// Prefix indicates a folder/subfolder in the bucket. A trailing '/' is
	// added by the operator if not provided. Templating is supported.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// Region is the S3 region name. Templating is supported.
	// +optional
	Region string `json:"region,omitempty"`

	// AccessKeyIDSecretKeyRef references a Secret key holding the S3 access
	// key id. Templating is supported on Name.
	AccessKeyIDSecretKeyRef MariaDBSecretKeySelector `json:"accessKeyIdSecretKeyRef"`

	// SecretAccessKeySecretKeyRef references a Secret key holding the S3
	// secret access key. Templating is supported on Name.
	SecretAccessKeySecretKeyRef MariaDBSecretKeySelector `json:"secretAccessKeySecretKeyRef"`

	// SessionTokenSecretKeyRef references a Secret key holding an optional
	// S3 session token. Templating is supported on Name.
	// +optional
	SessionTokenSecretKeyRef *MariaDBSecretKeySelector `json:"sessionTokenSecretKeyRef,omitempty"`

	// TLS configures TLS settings used by the operator when reaching the S3
	// endpoint.
	// +optional
	TLS *MariaDBS3TLS `json:"tls,omitempty"`
}

// MariaDBSecretKeySelector mirrors k8s.mariadb.com SecretKeySelector. The
// underlying Secret must live in the application's namespace.
type MariaDBSecretKeySelector struct {
	// Name is the Secret name. Templating is supported.
	Name string `json:"name"`

	// Key is the key within the Secret holding the value.
	Key string `json:"key"`
}

// MariaDBS3TLS mirrors k8s.mariadb.com Backup.spec.storage.s3.tls.
type MariaDBS3TLS struct {
	// Enabled toggles TLS for connections to the S3 endpoint.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// CASecretKeyRef references a Secret carrying a PEM-encoded CA bundle
	// the operator should trust when reaching a self-signed S3 endpoint
	// (e.g. cozystack's seaweedfs-s3). Templating is supported on Name.
	// +optional
	CASecretKeyRef *MariaDBSecretKeySelector `json:"caSecretKeyRef,omitempty"`
}

// MariaDBStatus reports observed state for the strategy CR. Driver
// controllers surface diagnostic conditions here (e.g. validation issues).
type MariaDBStatus struct {
	// Conditions holds the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
