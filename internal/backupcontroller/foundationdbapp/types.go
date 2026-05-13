// SPDX-License-Identifier: Apache-2.0

// Package foundationdbapp declares the typed shape of the
// apps.cozystack.io/v1alpha1 FoundationDB CR that the FoundationDB backup
// driver reads. It carries only fields the driver touches (currently
// nothing under spec — the driver materialises FoundationDBBackup /
// FoundationDBRestore CRs against the operator API and never patches the
// cozystack-side application), but the type still serves as a typed
// application-side handle so the driver can fetch the CR via the typed
// client and surface NotFound semantics cleanly.
//
// Living in an internal package keeps this duplication out of the public
// api/apps/v1alpha1 module, which exists for external consumers (the
// cozystack-api server, in particular) and has its own release cadence.
//
// +groupName=apps.cozystack.io
// +versionName=v1alpha1
package foundationdbapp

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "apps.cozystack.io"
	Version   = "v1alpha1"
	Kind      = "FoundationDB"
	ListKind  = Kind + "List"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(Kind), &FoundationDB{})
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(ListKind), &FoundationDBList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type FoundationDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FoundationDBSpec `json:"spec,omitempty"`
}

type FoundationDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FoundationDB `json:"items"`
}

// FoundationDBSpec is intentionally empty: the driver does not currently
// read or patch any apps.cozystack.io/FoundationDB spec fields. It targets
// the downstream apps.foundationdb.org/FoundationDBCluster CR (rendered by
// the chart's HelmRelease) only as an existence check, and materialises
// FoundationDBBackup / FoundationDBRestore CRs through foundationdbtypes.
//
// Reserved for future fields the driver might need to read (e.g. a backup
// section). Add fields here only when the driver genuinely needs them.
type FoundationDBSpec struct{}
