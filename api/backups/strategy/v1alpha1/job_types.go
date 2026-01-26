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
			&Job{},
			&JobList{},
		)
		return nil
	})
}

const (
	JobStrategyKind = "Job"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Job defines a backup strategy using a one-shot Job
type Job struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   JobSpec   `json:"spec,omitempty"`
	Status JobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// JobList contains a list of backup Jobs.
type JobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Job `json:"items"`
}

// JobSpec specifies the desired behavior of a backup job.
type JobSpec struct {
	// Template holds a PodTemplateSpec with the right shape to
	// run a single pod to completion and create a tarball with
	// a given apps data. Helm-like Go templates are supported.
	// The values of the source application are available under
	// `.Values`. `.Release.Name` and `.Release.Namespace` are
	// also exported.
	Template corev1.PodTemplateSpec `json:"template"`
}

type JobStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
