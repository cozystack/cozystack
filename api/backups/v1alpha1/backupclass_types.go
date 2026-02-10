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
			&BackupClass{},
			&BackupClassList{},
		)
		return nil
	})
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// BackupClass defines a class of backup configurations that can be referenced
// by BackupJob and Plan resources. It encapsulates strategy and storage configuration
// per application type.
type BackupClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupClassSpec   `json:"spec,omitempty"`
	Status BackupClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupClassList contains a list of BackupClasses.
type BackupClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupClass `json:"items"`
}

// BackupClassSpec defines the desired state of a BackupClass.
type BackupClassSpec struct {
	// Strategies is a list of backup strategies, each matching a specific application type.
	Strategies []BackupClassStrategy `json:"strategies"`
}

// BackupClassStrategy defines a backup strategy for a specific application type.
type BackupClassStrategy struct {
	// StrategyRef references the driver-specific BackupStrategy (e.g., Velero).
	StrategyRef corev1.TypedLocalObjectReference `json:"strategyRef"`

	// Application specifies which application types this strategy applies to.
	Application ApplicationSelector `json:"application"`

	// Parameters holds strategy-specific and storage-specific parameters.
	// Common parameters include:
	// - backupStorageLocationName: Name of Velero BackupStorageLocation
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ApplicationSelector specifies which application types a strategy applies to.
type ApplicationSelector struct {
	// APIGroup is the API group of the application.
	// If not specified, defaults to "apps.cozystack.io".
	// +optional
	APIGroup *string `json:"apiGroup,omitempty"`

	// Kind is the kind of the application (e.g., VirtualMachine, MariaDB).
	Kind string `json:"kind"`
}

// BackupClassStatus defines the observed state of a BackupClass.
type BackupClassStatus struct {
	// Conditions represents the latest available observations of a BackupClass's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
