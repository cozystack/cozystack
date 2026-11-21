// SPDX-License-Identifier: Apache-2.0

// Package mongodbapp declares the typed shape of the apps.cozystack.io/v1alpha1
// MongoDB CR that the MongoDB backup driver reads. It carries only the spec
// fields the driver reads (see MongoDBSpec), since the driver drives the
// downstream psmdb.percona.com CRs directly via psmdbtypes, but the type
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

// MongoDBSpec mirrors only the apps.cozystack.io/MongoDB spec fields the driver
// reads. It drives the downstream psmdb.percona.com CRs directly, so it keeps
// no other spec state here.
type MongoDBSpec struct {
	Backup MongoDBBackupSpec `json:"backup,omitempty"`
	// Databases is read for its keys alone. A to-copy restore copies the
	// databases the target application declares here, which are the ones the
	// chart grants the target's own users their roles on. It stays opaque so an
	// application with an unexpected shape here fails only the restore that
	// reads it, not the decoding of every MongoDB application in the cache.
	Databases *runtime.RawExtension `json:"databases,omitempty"`
}

// MongoDBBackupSpec carries the backup fields the driver reads off the app CR.
// UseSystemBucket is the tenant's opt-in to the platform system bucket: on that
// flow the chart leaves spec.backup.storages unset and the driver injects the
// storage from the strategy coordinates at BackupJob time. The driver keys its
// injection and its snapshot fallback off this flag, not off whatever storage
// happens to be on the live cluster, so a legacy app that ships its own static
// storage (UseSystemBucket=false) is never touched.
type MongoDBBackupSpec struct {
	UseSystemBucket bool `json:"useSystemBucket,omitempty"`
}
