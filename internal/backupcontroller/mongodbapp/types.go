// SPDX-License-Identifier: Apache-2.0

// Package mongodbapp declares the typed shape of the apps.cozystack.io/v1alpha1
// MongoDB CR that the MongoDB backup driver reads. It carries only fields the
// driver touches (currently nothing under spec — the driver drives the
// downstream psmdb.percona.com CRs directly via psmdbtypes), but the type
// still serves as a typed application-side handle so the driver can fetch the
// CR via the typed client and surface NotFound semantics cleanly. Mirrors
// mariadbapp.
//
// Living in an internal package keeps this duplication out of the public
// api/apps/v1alpha1 module, which exists for external consumers (the
// cozystack-api server, in particular) and has its own release cadence.
//
// +groupName=apps.cozystack.io
// +versionName=v1alpha1
package mongodbapp

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "apps.cozystack.io"
	Version   = "v1alpha1"
	Kind      = "MongoDB"
	ListKind  = Kind + "List"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(Kind), &MongoDB{})
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(ListKind), &MongoDBList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type MongoDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MongoDBSpec `json:"spec,omitempty"`
}

type MongoDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDB `json:"items"`
}

// MongoDBSpec is intentionally empty: the driver does not currently read or
// patch any apps.cozystack.io/MongoDB spec field. It reads the downstream
// psmdb.percona.com/PerconaServerMongoDB CR (rendered by the chart's
// HelmRelease) for existence + backup-enabled gating, and drives
// PerconaServerMongoDBBackup / PerconaServerMongoDBRestore CRs; it never
// mutates the app CR.
//
// Reserved for future fields the driver might need to read. Add fields here
// only when the driver genuinely needs them.
type MongoDBSpec struct{}
