// SPDX-License-Identifier: Apache-2.0

// Package buckettypes declares the minimum subset of the COSI
// objectstorage.k8s.io/v1alpha1 CRD shape that the Bucket backup driver
// operates on: it reads a BucketClaim's driver-assigned bucket name and
// provisions the BucketAccess it needs to reach the source (read) and a
// to-copy target (write). Mirroring only the fields the driver touches keeps
// the upstream COSI Go API - and its transitive imports - out of the
// controller and its tests.
//
// +groupName=objectstorage.k8s.io
// +versionName=v1alpha1
package buckettypes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "objectstorage.k8s.io"
	Version   = "v1alpha1"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&BucketClaim{}, &BucketClaimList{},
		&BucketAccess{}, &BucketAccessList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// ---------------------------------------------------------------------------
// BucketClaim
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type BucketClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BucketClaimSpec   `json:"spec,omitempty"`
	Status            BucketClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BucketClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketClaim `json:"items"`
}

// BucketClaimSpec mirrors the subset the driver reads. BucketClassName is the
// COSI class the claim was provisioned from; the driver derives the matching
// BucketAccessClass name from it (see deriveAccessClassName).
type BucketClaimSpec struct {
	BucketClassName string   `json:"bucketClassName,omitempty"`
	Protocols       []string `json:"protocols,omitempty"`
}

// BucketClaimStatus mirrors the subset the driver reads. BucketName is the
// driver-assigned S3 bucket name (not the Kubernetes object name); BucketReady
// gates backup readiness.
type BucketClaimStatus struct {
	BucketName  string `json:"bucketName,omitempty"`
	BucketReady bool   `json:"bucketReady,omitempty"`
}

// ---------------------------------------------------------------------------
// BucketAccess
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type BucketAccess struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BucketAccessSpec   `json:"spec,omitempty"`
	Status            BucketAccessStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BucketAccessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketAccess `json:"items"`
}

// BucketAccessSpec mirrors the fields the driver writes when it provisions a
// backup-scoped access grant. The COSI sidecar populates the Secret named by
// CredentialsSecretName with a single BucketInfo JSON document.
type BucketAccessSpec struct {
	BucketClaimName       string `json:"bucketClaimName,omitempty"`
	BucketAccessClassName string `json:"bucketAccessClassName,omitempty"`
	Protocol              string `json:"protocol,omitempty"`
	CredentialsSecretName string `json:"credentialsSecretName,omitempty"`
}

// BucketAccessStatus mirrors the readiness field the driver gates on.
type BucketAccessStatus struct {
	AccessGranted bool   `json:"accessGranted,omitempty"`
	AccountID     string `json:"accountID,omitempty"`
}
