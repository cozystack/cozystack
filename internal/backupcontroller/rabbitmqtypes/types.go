// SPDX-License-Identifier: Apache-2.0

// Package rabbitmqtypes declares the minimum subset of rabbitmq.com/v1beta1
// RabbitmqCluster the backupstrategy controller reads. The RabbitMQ backup
// driver never creates or patches operator CRs (it exports/imports definitions
// over the management API from a Job), so this carries only the status the
// driver gates on — whether every replica is ready — rather than pulling the
// full rabbitmq-cluster-operator Go API. Reads are lenient, so the hand-written
// partial is sufficient.
//
// +groupName=rabbitmq.com
// +versionName=v1beta1
package rabbitmqtypes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "rabbitmq.com"
	Version   = "v1beta1"

	// ConditionAllReplicasReady is the cluster-operator condition the driver
	// gates on: True once every RabbitMQ server replica is running (and the
	// management API is therefore serving). The driver defers a backup/restore
	// with a precise Ready=False message until this is True.
	ConditionAllReplicasReady = "AllReplicasReady"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&RabbitmqCluster{}, &RabbitmqClusterList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// +kubebuilder:object:root=true

// RabbitmqCluster carries only the status the driver reads for its readiness
// gate; the spec is intentionally omitted (the driver never writes the CR).
type RabbitmqCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            RabbitmqClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RabbitmqClusterList contains a list of RabbitmqCluster.
type RabbitmqClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RabbitmqCluster `json:"items"`
}

// RabbitmqClusterStatus mirrors the subset of the operator status the driver
// reads. Conditions decode as metav1.Condition (the operator's conditions are
// wire-compatible); the driver only inspects ConditionAllReplicasReady.
type RabbitmqClusterStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func (in *RabbitmqCluster) DeepCopyInto(out *RabbitmqCluster) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *RabbitmqCluster) DeepCopy() *RabbitmqCluster {
	if in == nil {
		return nil
	}
	out := new(RabbitmqCluster)
	in.DeepCopyInto(out)
	return out
}

func (in *RabbitmqCluster) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *RabbitmqClusterList) DeepCopyInto(out *RabbitmqClusterList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]RabbitmqCluster, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *RabbitmqClusterList) DeepCopy() *RabbitmqClusterList {
	if in == nil {
		return nil
	}
	out := new(RabbitmqClusterList)
	in.DeepCopyInto(out)
	return out
}

func (in *RabbitmqClusterList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *RabbitmqClusterStatus) DeepCopyInto(out *RabbitmqClusterStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *RabbitmqClusterStatus) DeepCopy() *RabbitmqClusterStatus {
	if in == nil {
		return nil
	}
	out := new(RabbitmqClusterStatus)
	in.DeepCopyInto(out)
	return out
}
