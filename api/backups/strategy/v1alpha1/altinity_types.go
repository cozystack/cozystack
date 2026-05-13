// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines strategy.backups.cozystack.io API types.
//
// Group: strategy.backups.cozystack.io
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
			&Altinity{},
			&AltinityList{},
		)
		return nil
	})
}

const (
	AltinityStrategyKind = "Altinity"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Altinity defines a backup strategy that delegates execution to Altinity's
// clickhouse-backup tool, run as a one-shot batch/v1.Job per BackupJob /
// RestoreJob. The strategy carries a templated PodTemplateSpec; the driver
// renders it against the source application and the resolved BackupClass
// parameters, wraps it in a Job, and surfaces completion as a Cozystack
// Backup artifact.
type Altinity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AltinitySpec   `json:"spec,omitempty"`
	Status AltinityStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AltinityList contains a list of Altinity backup strategies.
type AltinityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Altinity `json:"items"`
}

// AltinitySpec specifies the desired Altinity-driven backup strategy.
type AltinitySpec struct {
	// Template is the PodTemplateSpec the driver wraps in a batch/v1.Job
	// for both backup and restore runs. Helm-style Go templates are supported
	// in every string field. The available context is:
	//   .Application - the application object (apps.cozystack.io/ClickHouse)
	//   .Release.Name, .Release.Namespace - shorthand for the application
	//                                       name/namespace
	//   .Mode        - "backup" or "restore"
	//   .Parameters  - map[string]string from the matched BackupClassStrategy
	//   .Backup      - the source Backup metadata (only set on restore);
	//                  exposes .Name, .Namespace, and
	//                  .ApplicationRef.{APIGroup,Kind,Name} so to-copy
	//                  restores can address the source release.
	Template corev1.PodTemplateSpec `json:"template"`
}

// AltinityStatus reports observed state for the strategy CR. Driver
// controllers surface diagnostic conditions here (e.g. validation issues).
type AltinityStatus struct {
	// Conditions holds the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
