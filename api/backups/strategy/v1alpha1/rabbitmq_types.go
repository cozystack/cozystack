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
			&Rabbitmq{},
			&RabbitmqList{},
		)
		return nil
	})
}

const (
	RabbitmqStrategyKind = "Rabbitmq"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Rabbitmq defines a backup strategy that exports and imports RabbitMQ broker
// DEFINITIONS (vhosts, users, permissions, queues, exchanges, bindings,
// policies, parameters) via the management HTTP API, run as a one-shot
// batch/v1.Job per BackupJob / RestoreJob. Message payloads are out of scope -
// RabbitMQ has no consistent online export of them, so a full-data backup is a
// Velero volume snapshot, not this strategy. The driver gates on the target
// RabbitmqCluster being ready, wraps the templated PodTemplateSpec in a Job,
// and records the stored object on the Cozystack Backup artifact.
//
// Restore is a MERGE, not a reset: it POSTs the definitions to /api/definitions,
// which creates or updates what is in the export and never deletes anything, so
// objects created after the backup survive the restore and the broker is not
// returned to its exact backup-time state. A to-copy restore also imports the
// source's users (with password hashes) into the target.
type Rabbitmq struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RabbitmqSpec   `json:"spec,omitempty"`
	Status RabbitmqStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RabbitmqList contains a list of Rabbitmq backup strategies.
type RabbitmqList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Rabbitmq `json:"items"`
}

// RabbitmqSpec specifies the desired Rabbitmq-driven backup strategy.
type RabbitmqSpec struct {
	// Template is the PodTemplateSpec the driver wraps in a batch/v1.Job for
	// both backup and restore runs. Helm-style Go templates are supported in
	// every string field. The available context is:
	//   .Application - the application object (apps.cozystack.io/RabbitMQ)
	//   .Release.Name, .Release.Namespace - shorthand for the application
	//                                       name/namespace
	//   .Mode        - "backup" or "restore"
	//   .BackupName  - the per-run identity used to scope the object key so
	//                  distinct backups of one application do not overwrite
	//                  each other: the BackupJob name on backup, the source
	//                  Backup name on restore.
	//   .ArtifactURI - the stored object's location and single source of truth:
	//                  the rendered ArtifactURITemplate on backup, and the value
	//                  recorded on the source Backup's status.artifact.uri on
	//                  restore (so restore reads the exact object backup wrote).
	//   .Parameters  - map[string]string from the matched BackupClassStrategy
	//   .Backup      - the source Backup metadata (only set on restore);
	//                  exposes .Name, .Namespace, and
	//                  .ApplicationRef.{APIGroup,Kind,Name} so a to-copy
	//                  restore can address the source release's object key.
	//
	// The template is stored schemaless (its content is preserved but not
	// validated by the apiserver): the driver renders and validates the pod
	// spec, and generating the full PodTemplateSpec OpenAPI schema here would
	// add ~590 KB to this CRD. The backupstrategy-controller chart inlines
	// every definitions/*.yaml into one Helm release, whose state Secret is
	// capped at 1 MiB - a fourth full PodTemplateSpec schema (after Altinity,
	// Job and FoundationDB) pushes that Secret over the cap and fails install.
	//
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Template corev1.PodTemplateSpec `json:"template"`

	// ArtifactURITemplate is the object location the backup writes to, rendered
	// against the run context and recorded on the produced Backup's
	// status.artifact.uri. It is the single source of truth for where the
	// export lives: the backup Job writes exactly this object, and restore reads
	// the recorded value back rather than reconstructing a key. It is REQUIRED
	// for backups - a BackupJob whose resolved strategy leaves it empty fails,
	// because nothing would record where the export was stored. Ignored on
	// restore (the recorded value is used instead).
	// +optional
	ArtifactURITemplate string `json:"artifactURITemplate,omitempty"`
}

// RabbitmqStatus reports observed state for the strategy CR.
type RabbitmqStatus struct {
	// Conditions holds the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
