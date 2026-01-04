// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines backups.cozystack.io API types.
//
// Group: backups.cozystack.io
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
			&BackupJob{},
			&BackupJobList{},
		)
		return nil
	})
}

const (
	OwningJobNameLabel      = thisGroup + "/owned-by.BackupJobName"
	OwningJobNamespaceLabel = thisGroup + "/owned-by.BackupJobNamespace"
)

// BackupJobPhase represents the lifecycle phase of a BackupJob.
type BackupJobPhase string

const (
	BackupJobPhaseEmpty     BackupJobPhase = ""
	BackupJobPhasePending   BackupJobPhase = "Pending"
	BackupJobPhaseRunning   BackupJobPhase = "Running"
	BackupJobPhaseSucceeded BackupJobPhase = "Succeeded"
	BackupJobPhaseFailed    BackupJobPhase = "Failed"
)

// BackupJobSpec describes the execution of a single backup operation.
type BackupJobSpec struct {
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
}

// BackupJobStatus represents the observed state of a BackupJob.
type BackupJobStatus struct {
	// Phase is a high-level summary of the run's state.
	// Typical values: Pending, Running, Succeeded, Failed.
	// +optional
	Phase BackupJobPhase `json:"phase,omitempty"`

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

	// Conditions represents the latest available observations of a BackupJob's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// The field indexing on applicationRef will be needed later to display per-app backup resources.

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",priority=0
// +kubebuilder:selectablefield:JSONPath=`.spec.applicationRef.apiGroup`
// +kubebuilder:selectablefield:JSONPath=`.spec.applicationRef.kind`
// +kubebuilder:selectablefield:JSONPath=`.spec.applicationRef.name`

// BackupJob represents a single execution of a backup.
// It is typically created by a Plan controller when a schedule fires.
type BackupJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupJobSpec   `json:"spec,omitempty"`
	Status BackupJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupJobList contains a list of BackupJobs.
type BackupJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupJob `json:"items"`
}
