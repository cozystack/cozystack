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
			&RestoreJob{},
			&RestoreJobList{},
		)
		return nil
	})
}

// RestoreJobPhase represents the lifecycle phase of a RestoreJob.
type RestoreJobPhase string

const (
	RestoreJobPhaseEmpty     RestoreJobPhase = ""
	RestoreJobPhasePending   RestoreJobPhase = "Pending"
	RestoreJobPhaseRunning   RestoreJobPhase = "Running"
	RestoreJobPhaseSucceeded RestoreJobPhase = "Succeeded"
	RestoreJobPhaseFailed    RestoreJobPhase = "Failed"
)

// RestoreJobSpec describes the execution of a single restore operation.
type RestoreJobSpec struct {
	// BackupRef refers to the Backup that should be restored.
	BackupRef corev1.LocalObjectReference `json:"backupRef"`

	// TargetApplicationRef refers to the application into which the backup
	// should be restored. If omitted, the driver SHOULD restore into the same
	// application as referenced by backup.spec.applicationRef.
	// +optional
	TargetApplicationRef *corev1.TypedLocalObjectReference `json:"targetApplicationRef,omitempty"`
}

// RestoreJobStatus represents the observed state of a RestoreJob.
type RestoreJobStatus struct {
	// Phase is a high-level summary of the run's state.
	// Typical values: Pending, Running, Succeeded, Failed.
	// +optional
	Phase RestoreJobPhase `json:"phase,omitempty"`

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

	// Conditions represents the latest available observations of a RestoreJob's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// RestoreJob represents a single execution of a restore from a Backup.
type RestoreJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RestoreJobSpec   `json:"spec,omitempty"`
	Status RestoreJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RestoreJobList contains a list of RestoreJobs.
type RestoreJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RestoreJob `json:"items"`
}
