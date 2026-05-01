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
			&CNPG{},
			&CNPGList{},
		)
		return nil
	})
}

const (
	CNPGStrategyKind = "CNPG"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// CNPG defines a backup strategy that delegates execution to the
// CloudNativePG operator (postgresql.cnpg.io). The strategy carries a
// templated barmanObjectStore configuration that the driver injects into
// the live cnpg.io Cluster of the application; backup runs are produced as
// postgresql.cnpg.io/Backup objects and surfaced as Cozystack Backup
// artifacts.
type CNPG struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CNPGSpec   `json:"spec,omitempty"`
	Status CNPGStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CNPGList contains a list of CNPG backup strategies.
type CNPGList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CNPG `json:"items"`
}

// CNPGSpec specifies the desired CloudNativePG-driven backup strategy.
type CNPGSpec struct {
	// Template carries the templated barmanObjectStore configuration applied
	// to the live cnpg.io Cluster. String fields support Helm-style Go
	// templating with two top-level values:
	//   .Application - the application object (apps.cozystack.io)
	//   .Parameters  - the parameters from the matched BackupClassStrategy
	Template CNPGTemplate `json:"template"`
}

// CNPGTemplate describes the templated CNPG-specific configuration.
type CNPGTemplate struct {
	// BarmanObjectStore is the templated CloudNativePG barmanObjectStore
	// configuration. Field semantics mirror
	// postgresql.cnpg.io/v1.BarmanObjectStoreConfiguration.
	BarmanObjectStore BarmanObjectStoreTemplate `json:"barmanObjectStore"`

	// ServerName overrides the Barman server name used as the storage path
	// prefix. When empty, the driver defaults it to the application name.
	// Templating is supported.
	// +optional
	ServerName string `json:"serverName,omitempty"`
}

// BarmanObjectStoreTemplate is a typed, kubebuilder-validated mirror of the
// CloudNativePG barmanObjectStore configuration. The driver translates this
// struct into the unstructured shape expected by postgresql.cnpg.io.
type BarmanObjectStoreTemplate struct {
	// DestinationPath is the S3 (or compatible) destination URI, for example
	// "s3://my-bucket/path/". Templating is supported.
	// +kubebuilder:validation:MinLength=1
	DestinationPath string `json:"destinationPath"`

	// EndpointURL is the S3 endpoint URL. Templating is supported.
	// +optional
	EndpointURL string `json:"endpointURL,omitempty"`

	// S3Credentials references a Secret that holds the S3 access keys. The
	// Secret must live in the application's namespace. Templating is
	// supported on Name and key fields.
	// +optional
	S3Credentials *S3CredentialsTemplate `json:"s3Credentials,omitempty"`

	// RetentionPolicy is a Barman retention policy expression (e.g. "30d").
	// +optional
	RetentionPolicy string `json:"retentionPolicy,omitempty"`

	// Wal carries optional Barman WAL settings.
	// +optional
	Wal *BarmanWalTemplate `json:"wal,omitempty"`

	// Data carries optional Barman base-backup settings.
	// +optional
	Data *BarmanDataTemplate `json:"data,omitempty"`
}

// S3CredentialsTemplate references a Secret with S3 credentials. Default
// keys mirror the chart's existing convention so a Bucket-derived Secret
// works without any field overrides.
type S3CredentialsTemplate struct {
	// SecretRef is a reference to the Secret in the application's namespace
	// that holds the credentials.
	SecretRef corev1.LocalObjectReference `json:"secretRef"`

	// AccessKeyIDKey is the key within the Secret holding the access key ID.
	// Defaults to AWS_ACCESS_KEY_ID.
	// +optional
	AccessKeyIDKey string `json:"accessKeyIDKey,omitempty"`

	// SecretAccessKeyKey is the key within the Secret holding the secret access key.
	// Defaults to AWS_SECRET_ACCESS_KEY.
	// +optional
	SecretAccessKeyKey string `json:"secretAccessKeyKey,omitempty"`
}

// BarmanWalTemplate exposes the Barman WAL knobs.
type BarmanWalTemplate struct {
	// Compression algorithm for WAL files (e.g. "gzip", "bzip2", "snappy").
	// +optional
	Compression string `json:"compression,omitempty"`

	// Encryption algorithm for WAL files.
	// +optional
	Encryption string `json:"encryption,omitempty"`
}

// BarmanDataTemplate exposes the Barman base-backup knobs.
type BarmanDataTemplate struct {
	// Compression algorithm for base backups.
	// +optional
	Compression string `json:"compression,omitempty"`

	// Encryption algorithm for base backups.
	// +optional
	Encryption string `json:"encryption,omitempty"`

	// Jobs controls the number of parallel jobs to use during backup.
	// +optional
	Jobs *int32 `json:"jobs,omitempty"`
}

// CNPGStatus reports observed state for the strategy CR. Driver controllers
// surface diagnostic conditions here (e.g. validation issues).
type CNPGStatus struct {
	// Conditions holds the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
