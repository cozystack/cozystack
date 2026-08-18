// SPDX-License-Identifier: Apache-2.0

// Package cnpgtypes declares the minimum subset of postgresql.cnpg.io/v1
// CRD shape that the backupstrategy controller operates on. Avoids pulling
// the full upstream CloudNativePG Go API (which transitively imports a
// large surface area we do not need) while letting us drop
// unstructured.Unstructured from the controller and tests.
//
// +groupName=postgresql.cnpg.io
// +versionName=v1
package cnpgtypes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "postgresql.cnpg.io"
	Version   = "v1"

	// BarmanGroupName / BarmanVersion identify the Barman Cloud plugin API
	// (github.com/cloudnative-pg/plugin-barman-cloud). The ObjectStore CRD it
	// ships carries the S3/barman configuration the driver used to inline into
	// spec.backup.barmanObjectStore; native barman is deprecated in CNPG 1.27
	// and removed in 1.29.
	BarmanGroupName = "barmancloud.cnpg.io"
	BarmanVersion   = "v1"

	// PluginName is the CNPG-I plugin name the barman-cloud plugin registers
	// under. It MUST match the plugin Service's cnpg.io/pluginName label so
	// CNPG routes backup/WAL/recovery through it.
	PluginName = "barman-cloud.cloudnative-pg.io"

	// BackupMethodPlugin is the postgresql.cnpg.io/Backup spec.method value
	// that delegates the run to a CNPG-I plugin (here barman-cloud).
	BackupMethodPlugin = "plugin"
)

var (
	GroupVersion       = schema.GroupVersion{Group: GroupName, Version: Version}
	BarmanGroupVersion = schema.GroupVersion{Group: BarmanGroupName, Version: BarmanVersion}
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme        = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&Cluster{}, &ClusterList{},
		&Backup{}, &BackupList{},
	)
	scheme.AddKnownTypes(BarmanGroupVersion,
		&ObjectStore{}, &ObjectStoreList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	metav1.AddToGroupVersion(scheme, BarmanGroupVersion)
	return nil
}

// +kubebuilder:object:root=true
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterSpec   `json:"spec,omitempty"`
	Status            ClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}

type ClusterSpec struct {
	Backup    *BackupConfiguration    `json:"backup,omitempty"`
	Bootstrap *BootstrapConfiguration `json:"bootstrap,omitempty"`
	// Plugins wires CNPG-I plugins (here barman-cloud) onto the Cluster. The
	// driver SSA-patches exactly this field for the platform-managed backup
	// flow, replacing the deprecated spec.backup.barmanObjectStore path.
	Plugins []PluginConfiguration `json:"plugins,omitempty"`
}

// PluginConfiguration references a CNPG-I plugin from a Cluster. For
// barman-cloud, Parameters carries barmanObjectName (the ObjectStore CR name)
// and optionally serverName (the per-server folder under destinationPath).
type PluginConfiguration struct {
	Name string `json:"name"`
	// IsWALArchiver marks this plugin as the WAL archiver. Exactly one plugin
	// entry must set it for continuous archiving / PITR to work.
	IsWALArchiver *bool             `json:"isWALArchiver,omitempty"`
	Parameters    map[string]string `json:"parameters,omitempty"`
}

type ClusterStatus struct {
	Phase string `json:"phase,omitempty"`
}

type BackupConfiguration struct {
	BarmanObjectStore *BarmanObjectStoreConfiguration `json:"barmanObjectStore,omitempty"`
	RetentionPolicy   string                          `json:"retentionPolicy,omitempty"`
}

type BarmanObjectStoreConfiguration struct {
	DestinationPath string `json:"destinationPath,omitempty"`
	EndpointURL     string `json:"endpointURL,omitempty"`
	// EndpointCA references a Secret/ConfigMap key carrying a CA bundle the
	// Barman client should trust when reaching a self-signed S3 endpoint
	// (e.g. cozystack's seaweedfs-s3, which is TLS-only with a per-cluster
	// internal CA). When nil, Barman falls back to system trust store.
	EndpointCA    *SecretKeySelector       `json:"endpointCA,omitempty"`
	ServerName    string                   `json:"serverName,omitempty"`
	S3Credentials *S3Credentials           `json:"s3Credentials,omitempty"`
	Wal           *WalBackupConfiguration  `json:"wal,omitempty"`
	Data          *DataBackupConfiguration `json:"data,omitempty"`
}

type S3Credentials struct {
	AccessKeyID     *SecretKeySelector `json:"accessKeyId,omitempty"`
	SecretAccessKey *SecretKeySelector `json:"secretAccessKey,omitempty"`
}

type SecretKeySelector struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key,omitempty"`
}

type WalBackupConfiguration struct {
	Compression string `json:"compression,omitempty"`
	Encryption  string `json:"encryption,omitempty"`
}

type DataBackupConfiguration struct {
	Compression string `json:"compression,omitempty"`
	Encryption  string `json:"encryption,omitempty"`
	Jobs        *int32 `json:"jobs,omitempty"`
}

type BootstrapConfiguration struct {
	Recovery *RecoverySource `json:"recovery,omitempty"`
}

type RecoverySource struct {
	Source         string          `json:"source,omitempty"`
	RecoveryTarget *RecoveryTarget `json:"recoveryTarget,omitempty"`
}

type RecoveryTarget struct {
	TargetTime string `json:"targetTime,omitempty"`
}

// +kubebuilder:object:root=true
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BackupSpec   `json:"spec,omitempty"`
	Status            BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}

type BackupSpec struct {
	Method  string           `json:"method,omitempty"`
	Cluster ClusterReference `json:"cluster,omitempty"`
	// PluginConfiguration selects the CNPG-I plugin that executes the backup
	// when Method is "plugin". For barman-cloud, Name is the plugin name.
	PluginConfiguration *BackupPluginConfiguration `json:"pluginConfiguration,omitempty"`
}

// BackupPluginConfiguration names the plugin a Backup run delegates to.
type BackupPluginConfiguration struct {
	Name       string            `json:"name"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

type ClusterReference struct {
	Name string `json:"name,omitempty"`
}

// ObjectStore is the minimal subset of the barmancloud.cnpg.io/v1 ObjectStore
// CRD the driver writes. spec.configuration carries the S3/barman settings
// (the same shape native barmanObjectStore used, minus serverName which the
// plugin forbids there and takes from the Cluster plugin parameter instead);
// spec.retentionPolicy is top-level.
//
// +kubebuilder:object:root=true
type ObjectStore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ObjectStoreSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type ObjectStoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObjectStore `json:"items"`
}

type ObjectStoreSpec struct {
	Configuration BarmanObjectStoreConfiguration `json:"configuration"`
	// RetentionPolicy is a barman retention expression validated by the plugin
	// CRD against ^[1-9][0-9]*[dwm]$ (e.g. "30d").
	RetentionPolicy string `json:"retentionPolicy,omitempty"`
	// InstanceSidecarConfiguration passes settings to the barman-cloud sidecar
	// that the plugin injects into the CNPG pods. Only the fields we set are
	// modelled (see the upstream ObjectStore CRD for the full type).
	InstanceSidecarConfiguration *InstanceSidecarConfiguration `json:"instanceSidecarConfiguration,omitempty"`
}

// InstanceSidecarConfiguration is a minimal mirror of the barman-cloud
// ObjectStore's spec.instanceSidecarConfiguration (only spec.env).
type InstanceSidecarConfiguration struct {
	Env []EnvVar `json:"env,omitempty"`
}

// EnvVar is a minimal corev1.EnvVar (name/value only) — enough to pass a
// literal environment variable to the barman-cloud sidecar.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

type BackupStatus struct {
	Phase     string       `json:"phase,omitempty"`
	Error     string       `json:"error,omitempty"`
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// BeginWal and EndWal record the WAL boundaries the backup spans.
	// EndWal is the WAL filename CNPG flushes after pg_stop_backup; CNPG
	// only sets it once the upload to object storage succeeds, so the
	// presence of EndWal alongside Phase=completed is what the in-place
	// restore gate uses to confirm the backup is restorable before it
	// destroys the source PVCs.
	BeginWal string `json:"beginWal,omitempty"`
	EndWal   string `json:"endWal,omitempty"`
}
