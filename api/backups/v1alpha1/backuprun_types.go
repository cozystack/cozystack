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

// BackupRunPhase represents the lifecycle phase of a BackupRun.
type BackupRunPhase string

const (
	BackupRunPhaseEmpty     BackupRunPhase = ""
	BackupRunPhasePending   BackupRunPhase = "Pending"
	BackupRunPhaseRunning   BackupRunPhase = "Running"
	BackupRunPhaseSucceeded BackupRunPhase = "Succeeded"
	BackupRunPhaseFailed    BackupRunPhase = "Failed"
)

// BackupRunSpec describes the execution of a single backup operation.
type BackupRunSpec struct {
	// PlanRef refers to the Plan that requested this backup run.
	// For ad-hoc/manual backups, this can be omitted.
	// +optional
	PlanRef *corev1.LocalObjectReference `json:"planRef,omitempty"`

	// ApplicationRef holds a reference to the managed application whose state
	// is being backed up.
	ApplicationRef corev1.TypedLocalObjectReference `json:"applicationRef"`

	// StorageRef holds a reference to the Storage object that describes where
	// the backup will be stored.
	StorageRef corev1.TypedLocalObjectReference `json:"storageRef"`

	// StrategyRef holds a reference to the driver-specific BackupStrategy object
	// that describes how the backup should be created.
	StrategyRef corev1.TypedLocalObjectReference `json:"strategyRef"`

	// TriggeredBy describes what triggered this BackupRun (for example, "Plan",
	// "Manual", or an arbitrary string). For informational purposes only.
	// +optional
	TriggeredBy string `json:"triggeredBy,omitempty"`
}

// BackupRunStatus represents the observed state of a BackupRun.
type BackupRunStatus struct {
	// Phase is a high-level summary of the run's state.
	// Typical values: Pending, Running, Succeeded, Failed.
	// +optional
	Phase BackupRunPhase `json:"phase,omitempty"`

	// BackupRef refers to the Backup object created by this run, if any.
	// +optional
	BackupRef *corev1.LocalObjectReference `json:"backupRef,omitempty"`

	// StartedAt is the time at which the backup run started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is the time at which the backup run completed (successfully
	// or otherwise).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Message is a human-readable message indicating details about why the
	// backup run is in its current phase, if any.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represents the latest available observations of a BackupRun's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// BackupRun represents a single execution of a backup.
// It is typically created by a Plan controller when a schedule fires.
type BackupRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupRunSpec   `json:"spec,omitempty"`
	Status BackupRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupRunList contains a list of BackupRuns.
type BackupRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupRun `json:"items"`
}
