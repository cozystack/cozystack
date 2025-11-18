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

// RestoreRunPhase represents the lifecycle phase of a RestoreRun.
type RestoreRunPhase string

const (
	RestoreRunPhaseEmpty     RestoreRunPhase = ""
	RestoreRunPhasePending   RestoreRunPhase = "Pending"
	RestoreRunPhaseRunning   RestoreRunPhase = "Running"
	RestoreRunPhaseSucceeded RestoreRunPhase = "Succeeded"
	RestoreRunPhaseFailed    RestoreRunPhase = "Failed"
)

// RestoreRunSpec describes the execution of a single restore operation.
type RestoreRunSpec struct {
	// BackupRef refers to the Backup that should be restored.
	BackupRef corev1.LocalObjectReference `json:"backupRef"`

	// TargetApplicationRef refers to the application into which the backup
	// should be restored. If omitted, the driver SHOULD restore into the same
	// application as referenced by backup.spec.applicationRef.
	// +optional
	TargetApplicationRef *corev1.TypedLocalObjectReference `json:"targetApplicationRef,omitempty"`

	// StorageRefOverride can be used to override the Storage from which the
	// backup is read. If omitted, the driver SHOULD use backup.spec.storageRef.
	// +optional
	StorageRefOverride *corev1.TypedLocalObjectReference `json:"storageRefOverride,omitempty"`

	// StrategyRefOverride can be used to override the BackupStrategy to be used
	// for restore. If omitted, the driver SHOULD use backup.spec.strategyRef.
	// In most cases, this will be left empty and the original strategy will be used.
	// +optional
	StrategyRefOverride *corev1.TypedLocalObjectReference `json:"strategyRefOverride,omitempty"`

	// TriggeredBy describes what triggered this RestoreRun (for example,
	// "Manual" or an arbitrary string). For informational purposes only.
	// +optional
	TriggeredBy string `json:"triggeredBy,omitempty"`
}

// RestoreRunStatus represents the observed state of a RestoreRun.
type RestoreRunStatus struct {
	// Phase is a high-level summary of the run's state.
	// Typical values: Pending, Running, Succeeded, Failed.
	// +optional
	Phase RestoreRunPhase `json:"phase,omitempty"`

	// StartedAt is the time at which the restore run started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is the time at which the restore run completed (successfully
	// or otherwise).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Message is a human-readable message indicating details about why the
	// restore run is in its current phase, if any.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represents the latest available observations of a RestoreRun's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// RestoreRun represents a single execution of a restore from a Backup.
type RestoreRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RestoreRunSpec   `json:"spec,omitempty"`
	Status RestoreRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RestoreRunList contains a list of RestoreRuns.
type RestoreRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RestoreRun `json:"items"`
}
