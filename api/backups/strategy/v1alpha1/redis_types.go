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
			&Redis{},
			&RedisList{},
		)
		return nil
	})
}

const (
	RedisStrategyKind = "Redis"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Redis defines a backup strategy for a spotahome RedisFailover app. The
// spotahome operator ships no backup CRD, so the strategy runs a one-shot Job
// that dumps an RDB snapshot to object storage (and replays it on restore) -
// the same PodTemplateSpec model as the Job/Altinity strategies rather than an
// operator-CR delegation.
type Redis struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RedisSpec   `json:"spec,omitempty"`
	Status RedisStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RedisList contains a list of Redis backup strategies.
type RedisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Redis `json:"items"`
}

// RedisSpec specifies the desired behavior of a Redis backup strategy.
type RedisSpec struct {
	// Template holds a PodTemplateSpec with the right shape to run a single
	// pod to completion and dump (or restore) a Redis RDB snapshot to object
	// storage. Helm-like Go templates are supported in every string field and
	// are rendered against the following context:
	//   - `.Application` — the live application object (e.g.
	//     `.Application.spec.replicas`).
	//   - `.Release.Name` and `.Release.Namespace` — the application's name
	//     and namespace.
	//   - `.Mode` — `"backup"` or `"restore"`, identifying the run.
	//   - `.Parameters` — the resolved BackupClass strategy parameters.
	//   - `.Backup` — only on restore runs: `.Backup.Name`, `.Backup.Namespace`
	//     and `.Backup.ApplicationRef` describing the source backup, so a
	//     to-copy restore reads the SOURCE release's object-storage prefix.
	Template corev1.PodTemplateSpec `json:"template"`
}

type RedisStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
