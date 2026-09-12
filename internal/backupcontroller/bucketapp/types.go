// SPDX-License-Identifier: Apache-2.0

// Package bucketapp declares the typed shape of the apps.cozystack.io/v1alpha1
// Bucket CR that the Bucket backup driver reads. The driver only needs the
// application object as a namespaced handle (for NotFound semantics and for
// templating the destination prefix against .Application.metadata); the COSI
// BucketClaim/BucketAccess it operates on are modelled in buckettypes.
//
// Living in an internal package keeps this duplication out of the public
// api/apps/v1alpha1 module, which has its own release cadence.
//
// +groupName=apps.cozystack.io
// +versionName=v1alpha1
package bucketapp

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "apps.cozystack.io"
	Version   = "v1alpha1"
	Kind      = "Bucket"
	ListKind  = Kind + "List"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(Kind), &Bucket{})
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(ListKind), &BucketList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BucketSpec `json:"spec,omitempty"`
}

type BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bucket `json:"items"`
}

// BucketSpec mirrors the fields the driver reads. StoragePool and Locking are
// unused today but document the shape a future driver refinement (e.g.
// mirroring object-lock state) would consult.
type BucketSpec struct {
	// +optional
	StoragePool string `json:"storagePool,omitempty"`
	// +optional
	Locking bool `json:"locking,omitempty"`
}
