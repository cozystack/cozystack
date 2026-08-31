// SPDX-License-Identifier: Apache-2.0

// Package kafkatypes declares the minimum subset of the kafka.strimzi.io/v1beta2
// Kafka cluster CR the backupstrategy controller reads: the Ready condition,
// used only as a readiness precondition before the Kafka metadata backup/restore
// Job runs. Modelled as a partial (no spec) so we avoid pulling the full Strimzi
// Go API while dropping unstructured.Unstructured from the driver and its tests.
// The driver never writes this CR.
//
// +groupName=kafka.strimzi.io
// +versionName=v1beta2
// +kubebuilder:object:generate=true
package kafkatypes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "kafka.strimzi.io"
	Version   = "v1beta2"

	// ConditionTypeReady is the Strimzi cluster-operator condition that turns
	// True once the Kafka cluster (brokers, and in ZooKeeper mode ZooKeeper) is
	// up and serving. The Admin API the backup Job talks to is only reachable
	// then.
	ConditionTypeReady = "Ready"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&Kafka{}, &KafkaList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// +kubebuilder:object:root=true
type Kafka struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KafkaSpec   `json:"spec,omitempty"`
	Status            KafkaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KafkaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Kafka `json:"items"`
}

// KafkaSpec is intentionally empty: the driver reads the cluster only for its
// existence and Ready condition, never a spec field. Kept as a named struct so
// Kafka stays a regular kubebuilder object rather than a degenerate shell.
type KafkaSpec struct{}

type KafkaStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
