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

type PlanScheduleType string

const (
	PlanScheduleTypeEmpty PlanScheduleType = ""
	PlanScheduleTypeCron  PlanScheduleType = "cron"
)

// Condtions
const (
	PlanConditionError = "Error"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Plan describes the schedule, method and storage location for the
// backup of a given target application.
type Plan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PlanSpec   `json:"spec,omitempty"`
	Status PlanStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PlanList contains a list of backup Plans.
type PlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Plan `json:"items"`
}

// PlanSpec references the storage, the strategy, the application to be
// backed up and specifies the timetable on which the backups will run.
type PlanSpec struct {
	// ApplicationRef holds a reference to the managed application,
	// whose state and configuration must be backed up.
	ApplicationRef corev1.TypedLocalObjectReference `json:"applicationRef"`

	// StorageRef holds a reference to the Storage object that
	// describes the location where the backup will be stored.
	StorageRef corev1.TypedLocalObjectReference `json:"storageRef"`

	// StrategyRef holds a reference to the Strategy object that
	// describes, how a backup copy is to be created.
	StrategyRef corev1.TypedLocalObjectReference `json:"strategyRef"`

	// Schedule specifies when backup copies are created.
	Schedule PlanSchedule `json:"schedule"`
}

// PlanSchedule specifies when backup copies are created.
type PlanSchedule struct {
	// Type is the type of schedule specification. Supported values are
	// [`cron`]. If omitted, defaults to `cron`.
	// +optional
	Type PlanScheduleType `json:"type,omitempty"`

	// Cron contains the cron spec for scheduling backups. Must be
	// specified if the schedule type is `cron`. Since only `cron` is
	// supported, omitting this field is not allowed.
	// +optional
	Cron string `json:"cron,omitempty"`
}

type PlanStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
