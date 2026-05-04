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
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&Cluster{}, &ClusterList{},
		&Backup{}, &BackupList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
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
}

type ClusterStatus struct {
	Phase string `json:"phase,omitempty"`
}

type BackupConfiguration struct {
	BarmanObjectStore *BarmanObjectStoreConfiguration `json:"barmanObjectStore,omitempty"`
	RetentionPolicy   string                          `json:"retentionPolicy,omitempty"`
}

type BarmanObjectStoreConfiguration struct {
	DestinationPath string                          `json:"destinationPath,omitempty"`
	EndpointURL     string                          `json:"endpointURL,omitempty"`
	// EndpointCA references a Secret/ConfigMap key carrying a CA bundle the
	// Barman client should trust when reaching a self-signed S3 endpoint
	// (e.g. cozystack's seaweedfs-s3, which is TLS-only with a per-cluster
	// internal CA). When nil, Barman falls back to system trust store.
	EndpointCA      *SecretKeySelector              `json:"endpointCA,omitempty"`
	ServerName      string                          `json:"serverName,omitempty"`
	S3Credentials   *S3Credentials                  `json:"s3Credentials,omitempty"`
	Wal             *WalBackupConfiguration         `json:"wal,omitempty"`
	Data            *DataBackupConfiguration        `json:"data,omitempty"`
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
}

type ClusterReference struct {
	Name string `json:"name,omitempty"`
}

type BackupStatus struct {
	Phase     string       `json:"phase,omitempty"`
	Error     string       `json:"error,omitempty"`
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
}
