// SPDX-License-Identifier: Apache-2.0

// Package mariadbapp declares the typed shape of the apps.cozystack.io/v1alpha1
// MariaDB CR that the MariaDB backup driver reads. It carries only fields the
// driver touches (currently nothing under spec — the driver patches the
// downstream k8s.mariadb.com/MariaDB CR directly via mariadbtypes), but the
// type still serves as a typed application-side handle so the driver can
// fetch the CR via the typed client and surface NotFound semantics cleanly.
//
// Living in an internal package keeps this duplication out of the public
// api/apps/v1alpha1 module, which exists for external consumers (the
// cozystack-api server, in particular) and has its own release cadence.
//
// +groupName=apps.cozystack.io
// +versionName=v1alpha1
package mariadbapp

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "apps.cozystack.io"
	Version   = "v1alpha1"
	Kind      = "MariaDB"
	ListKind  = Kind + "List"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(Kind), &MariaDB{})
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(ListKind), &MariaDBList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type MariaDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MariaDBSpec `json:"spec,omitempty"`
}

type MariaDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MariaDB `json:"items"`
}

// MariaDBSpec is intentionally empty: the driver does not currently read or
// patch any apps.cozystack.io/MariaDB spec fields. The driver only reads the
// downstream k8s.mariadb.com/MariaDB CR (rendered by the chart's HelmRelease)
// for existence checks - restores create k8s.mariadb.com/Restore CRs whose
// mariaDbRef points at the live MariaDB; the operator replays the dump into
// the running instance, so the driver does NOT touch
// MariaDB.spec.bootstrapFrom or any other field on the operator CR (its
// RBAC carries no patch/update verbs on k8s.mariadb.com/mariadbs for that
// reason).
//
// Reserved for future fields the driver might need to read (e.g. a backup
// section). Add fields here only when the driver genuinely needs them.
type MariaDBSpec struct{}
