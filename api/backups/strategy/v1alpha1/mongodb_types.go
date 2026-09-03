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
			&MongoDB{},
			&MongoDBList{},
		)
		return nil
	})
}

const (
	MongoDBStrategyKind = "MongoDB"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// MongoDB defines a backup strategy that delegates execution to the Percona
// Server for MongoDB operator (psmdb.percona.com). The strategy carries a
// templated logical-backup configuration; the driver materialises
// PerconaServerMongoDBBackup objects per BackupJob and surfaces them as
// Cozystack Backup artifacts. Restores create a PerconaServerMongoDBRestore
// CR with clusterName pointing at the target cluster and a backupSource
// carrying the S3 destination read back from the source backup, so the same
// artifact restores in-place or into a differently-named instance.
//
// Unlike the MariaDB strategy, the storage config is NOT inlined here: psmdb
// resolves spec.storageName against the source cluster's spec.backup.storages,
// so the S3/credentials live on the PerconaServerMongoDB CR (the mongodb chart
// declares them when backup.enabled=true) and the strategy only names the
// storage to use.
type MongoDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MongoDBSpec   `json:"spec,omitempty"`
	Status MongoDBStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MongoDBList contains a list of MongoDB backup strategies.
type MongoDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDB `json:"items"`
}

// MongoDBSpec specifies the desired psmdb-operator-driven backup strategy.
type MongoDBSpec struct {
	// Template carries the templated PerconaServerMongoDBBackup configuration.
	// String fields support Helm-style Go templating with two top-level
	// values:
	//   .Application - the application object (apps.cozystack.io/MongoDB)
	//   .Parameters  - the parameters from the matched BackupClassStrategy.
	//                  These values MUST NOT carry credentials — the psmdb
	//                  storage (bucket/endpoint/credentialsSecret) lives on the
	//                  PerconaServerMongoDB CR, referenced here only by name.
	Template MongoDBTemplate `json:"template"`
}

// MongoDBTemplate describes the templated PerconaServerMongoDBBackup shape the
// driver renders per BackupJob.
type MongoDBTemplate struct {
	// StorageName names a storage entry declared in the source cluster's
	// spec.backup.storages. The mongodb chart declares "s3-storage" when
	// backup.enabled=true; leave empty to use that default. Templating is
	// supported.
	// +optional
	StorageName string `json:"storageName,omitempty"`

	// Type selects the backup type. Only "logical" is supported (logical
	// dumps are portable across clusters, which is what makes
	// restore-to-differently-named instances work). Defaults to "logical"
	// when empty.
	// +kubebuilder:validation:Enum=logical
	// +optional
	Type string `json:"type,omitempty"`

	// CompressionType is the pbm compression algorithm (e.g. "gzip", "zstd",
	// "s2", "snappy", "lz4", "pgzip", "none"). Defaults to the operator-side
	// default when empty. Templating is supported.
	// +optional
	CompressionType string `json:"compressionType,omitempty"`

	// CompressionLevel tunes the compression algorithm (algorithm-specific).
	// Left unset the operator picks its default.
	// +optional
	CompressionLevel *int `json:"compressionLevel,omitempty"`
}

// MongoDBStatus reports observed state for the strategy CR. Driver controllers
// surface diagnostic conditions here (e.g. validation issues).
type MongoDBStatus struct {
	// Conditions holds the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
