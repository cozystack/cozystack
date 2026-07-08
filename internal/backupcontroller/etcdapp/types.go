// SPDX-License-Identifier: Apache-2.0

// Package etcdapp declares the typed shape of the
// apps.cozystack.io/v1alpha1 Etcd CR that the Etcd backup driver reads.
// It carries only fields the driver touches (currently nothing under
// spec - the driver materialises etcd-operator.cozystack.io EtcdSnapshot CRs
// against the operator API and writes back through the typed EtcdCluster
// handle in etcdtypes), but the type still serves as a typed application-side
// handle so the driver can fetch the CR via the typed client and surface
// NotFound semantics cleanly.
//
// Living in an internal package keeps this duplication out of the public
// api/apps/v1alpha1 module, which exists for external consumers (the
// cozystack-api server, in particular) and has its own release cadence.
//
// +groupName=apps.cozystack.io
// +versionName=v1alpha1
package etcdapp

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "apps.cozystack.io"
	Version   = "v1alpha1"
	Kind      = "Etcd"
	ListKind  = Kind + "List"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(Kind), &Etcd{})
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(ListKind), &EtcdList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type Etcd struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EtcdSpec `json:"spec,omitempty"`
}

type EtcdList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Etcd `json:"items"`
}

// EtcdSpec is intentionally empty: the driver does not currently read or
// patch any apps.cozystack.io/Etcd spec fields. It uses the downstream
// etcd-operator.cozystack.io/EtcdCluster CR (rendered by the chart's
// HelmRelease) as the operational handle and materialises
// etcd-operator.cozystack.io/EtcdSnapshot CRs through etcdtypes.
//
// Reserved for future fields the driver might need to read. Add fields
// here only when the driver genuinely needs them.
type EtcdSpec struct{}
