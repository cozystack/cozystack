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
			&Kafka{},
			&KafkaList{},
		)
		return nil
	})
}

const (
	KafkaStrategyKind = "Kafka"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Kafka defines a backup strategy for the topic METADATA of a Cozystack-managed
// Kafka application: topic definitions and their configs (partitions,
// replication factor, per-topic config keys), read and re-applied through the
// Kafka Admin API. It is not a message-data backup and carries no consumer
// offsets, ACLs, quotas or KafkaUsers — those are out of scope for this
// strategy (message data belongs to a volume-snapshot strategy). Like the Job
// strategy, the driver renders spec.template into a one-shot batch/v1.Job for
// both backup and restore.
type Kafka struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KafkaSpec   `json:"spec,omitempty"`
	Status KafkaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KafkaList contains a list of Kafka backup strategies.
type KafkaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Kafka `json:"items"`
}

// KafkaSpec specifies the desired behavior of a Kafka metadata backup strategy.
type KafkaSpec struct {
	// Template holds a PodTemplateSpec run to completion to back up (or restore)
	// the application's topic metadata. Helm-like Go templates are supported in
	// every string field and are rendered against:
	//   - `.Application` — the live application object.
	//   - `.Release.Name` and `.Release.Namespace` — the application's name and
	//     namespace (the TARGET on restore).
	//   - `.Mode` — `"backup"` or `"restore"`.
	//   - `.BackupName` — the per-run identity: the BackupJob name on backup,
	//     the source Backup name on restore. Use it to scope the object key so
	//     distinct backups of one application do not overwrite each other.
	//   - `.Parameters` — the resolved BackupClass strategy parameters.
	//   - `.Backup` — only on restore: `.Backup.Name`, `.Backup.Namespace` and
	//     `.Backup.ApplicationRef` describing the source backup.
	//
	// Stored schemaless deliberately: the full PodTemplateSpec OpenAPI schema is
	// ~590 KB, and inlining it here (after Altinity/Job/FoundationDB) would push
	// the chart's Helm state Secret over the 1 MiB cap. See job_types.go for the
	// same field carried with its schema, and strategy-kafka-default.yaml for
	// the rendered pod.
	//
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Template corev1.PodTemplateSpec `json:"template"`

	// ArtifactURITemplate is rendered against the same context on a successful
	// backup and recorded on the produced Backup's status.artifact.uri, so the
	// stored object's location is introspectable. Ignored on restore; empty
	// leaves status.artifact unset.
	// +optional
	ArtifactURITemplate string `json:"artifactURITemplate,omitempty"`
}

type KafkaStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
