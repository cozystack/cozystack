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
			&Velero{},
			&VeleroList{},
		)
		return nil
	})
}

const (
	VeleroStrategyKind = "Velero"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Velero defines a backup strategy using Velero as the driver.
type Velero struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VeleroSpec   `json:"spec,omitempty"`
	Status VeleroStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VeleroList contains a list of Velero backup strategies.
type VeleroList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Velero `json:"items"`
}

// VeleroSpec specifies the desired strategy for backing up with Velero.
type VeleroSpec struct{}

type VeleroStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
