// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines backups.cozystack.io API types.
//
// Group: backups.cozystack.io
// Version: v1alpha1
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupPhase represents the lifecycle phase of a Backup.
type BackupPhase string

const (
	BackupPhaseEmpty   BackupPhase = ""
	BackupPhasePending BackupPhase = "Pending"
	BackupPhaseReady   BackupPhase = "Ready"
	BackupPhaseFailed  BackupPhase = "Failed"
)

// BackupArtifact describes the stored backup object (tarball, snapshot, etc.).
type BackupArtifact struct {
	// URI is a driver-/storage-specific URI pointing to the backup artifact.
	// For example: s3://bucket/prefix/file.tar.gz
	URI string `json:"uri"`

	// SizeBytes is the size of the artifact in bytes, if known.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Checksum is the checksum of the artifact, if computed.
	// For example: "sha256:<hex>".
	// +optional
	Checksum string `json:"checksum,omitempty"`
}

// BackupSpec describes an immutable backup artifact produced by a BackupJob.
type BackupSpec struct {
	// ApplicationRef refers to the application that was backed up.
	ApplicationRef corev1.TypedLocalObjectReference `json:"applicationRef"`

	// PlanRef refers to the Plan that produced this backup, if any.
	// For manually triggered backups, this can be omitted.
	// +optional
	PlanRef *corev1.LocalObjectReference `json:"planRef,omitempty"`

	// StorageRef refers to the Storage object that describes where the backup
	// artifact is stored.
	StorageRef corev1.TypedLocalObjectReference `json:"storageRef"`

	// StrategyRef refers to the driver-specific BackupStrategy that was used
	// to create this backup. This allows the driver to later perform restores.
	StrategyRef corev1.TypedLocalObjectReference `json:"strategyRef"`

	// TakenAt is the time at which the backup was taken (as reported by the
	// driver). It may differ slightly from metadata.creationTimestamp.
	TakenAt metav1.Time `json:"takenAt"`

	// DriverMetadata holds driver-specific, opaque metadata associated with
	// this backup (for example snapshot IDs, schema versions, etc.).
	// This data is not interpreted by the core backup controllers.
	// +optional
	DriverMetadata map[string]string `json:"driverMetadata,omitempty"`
}

// BackupStatus represents the observed state of a Backup.
type BackupStatus struct {
	// Phase is a simple, high-level summary of the backup's state.
	// Typical values are: Pending, Ready, Failed.
	// +optional
	Phase BackupPhase `json:"phase,omitempty"`

	// Artifact describes the stored backup object, if available.
	// +optional
	Artifact *BackupArtifact `json:"artifact,omitempty"`

	// Conditions represents the latest available observations of a Backup's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// Backup represents a single backup artifact for a given application.
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupSpec   `json:"spec,omitempty"`
	Status BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupList contains a list of Backups.
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}
