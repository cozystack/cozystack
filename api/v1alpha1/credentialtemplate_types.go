// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CredentialTemplateSpec describes one account of an application and the
// Secret that stores its password. A kind's chart renders one per account; a
// tenant cannot write it.
// +kubebuilder:validation:XValidation:rule="!has(self.bucketAccess) || self.format.name == 'plaintext'",message="a template with bucketAccess stores its keys in the plaintext format"
type CredentialTemplateSpec struct {
	// Username is the account name as the engine knows it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="username is immutable"
	Username string `json:"username"`

	// Format is how a password is stored in the account Secret.
	Format CredentialTemplateFormat `json:"format"`

	// Secret describes the account Secret beyond its fixed username and
	// password keys.
	// +optional
	Secret CredentialTemplateSecret `json:"secret,omitempty"`

	// ValuesPath, for an engine whose operator reads no Secret, asks for the
	// stored value to be pulled into the release's values under
	// <valuesPath>.u<sha256 hex of the username>. It starts with "_", which
	// the Application API hides on read and refuses on write.
	// +optional
	// +kubebuilder:validation:Pattern=`^_[A-Za-z0-9_]*$`
	ValuesPath string `json:"valuesPath,omitempty"`

	// BucketAccess makes the account engine-issued: it names the BucketAccess
	// whose COSI grant issues the account's keys, which the platform cannot
	// choose. cozystack-controller creates and owns it, re-creates it on a
	// mint, and deletes it on a revoke. The keys are stored as they are
	// issued, so the format is plaintext.
	// +optional
	BucketAccess *CredentialTemplateBucketAccess `json:"bucketAccess,omitempty"`
}

// CredentialTemplateBucketAccess is the BucketAccess an engine-issued
// template stands for. Its credentials Secret carries the BucketAccess's own name.
// +kubebuilder:validation:XValidation:rule="self.name == oldSelf.name",message="bucketAccess.name is immutable"
type CredentialTemplateBucketAccess struct {
	// Name of the BucketAccess and of the Secret the COSI sidecar writes.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// BucketClaimName is the BucketClaim the access is granted on.
	// +kubebuilder:validation:MinLength=1
	BucketClaimName string `json:"bucketClaimName"`
	// BucketAccessClassName is the class that sets the access policy.
	// +kubebuilder:validation:MinLength=1
	BucketAccessClassName string `json:"bucketAccessClassName"`
}

// CredentialTemplateFormat names a stored form of a password.
type CredentialTemplateFormat struct {
	// Name is a built-in format: scram-sha-256, mysql-native, sha256 or
	// plaintext.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// CredentialTemplateSecret describes the account Secret.
type CredentialTemplateSecret struct {
	// Labels are put on the account Secret, for an operator that notices a
	// changed Secret only when it carries a label.
	// +optional
	// +kubebuilder:validation:XValidation:rule="!('credentials.cozystack.io/account' in self)",message="the credentials.cozystack.io/account label is reserved"
	Labels map[string]string `json:"labels,omitempty"`
}

// CredentialTemplateStatus mirrors the record the account Secret keeps, so the
// Credential view reads templates only.
type CredentialTemplateStatus struct {
	// SecretResourceVersion is the resourceVersion of the account Secret this
	// status mirrors; empty while the account has no Secret.
	// +optional
	SecretResourceVersion string `json:"secretResourceVersion,omitempty"`

	// Password says where the live password came from.
	// +optional
	Password CredentialTemplatePasswordState `json:"password,omitempty"`

	// History holds the last ten operations, newest first.
	// +optional
	// +listType=atomic
	History []CredentialTemplateHistoryEntry `json:"history,omitempty"`

	// ObservedGeneration is the generation the controller last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// CredentialTemplatePasswordState records the operation that set the live
// password.
type CredentialTemplatePasswordState struct {
	// Origin is NotIssued, Migrated, Minted or Revoked.
	// +optional
	Origin string `json:"origin,omitempty"`
	// +optional
	At *metav1.Time `json:"at,omitempty"`
	// +optional
	By string `json:"by,omitempty"`
}

// CredentialTemplateHistoryEntry is one recorded operation on the account.
type CredentialTemplateHistoryEntry struct {
	// Operation is Create, Migrate, Mint or Revoke.
	Operation string      `json:"operation"`
	At        metav1.Time `json:"at"`
	By        string      `json:"by"`
}

// Condition types a CredentialTemplate reports.
const (
	// CredentialTemplateSecretConflict: a Secret at the account Secret's name
	// that the controller did not create or adopt; it is left alone.
	CredentialTemplateSecretConflict = "SecretConflict"
	// CredentialTemplateUnknownFormat: the format names no built-in format.
	CredentialTemplateUnknownFormat = "UnknownFormat"
	// CredentialTemplateBucketAccessConflict: a BucketAccess at the name an
	// engine-issued template names that the controller did not create or
	// adopt; it is left alone.
	CredentialTemplateBucketAccessConflict = "BucketAccessConflict"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="User",type="string",JSONPath=".spec.username"
// +kubebuilder:printcolumn:name="Format",type="string",JSONPath=".spec.format.name"
// +kubebuilder:printcolumn:name="Origin",type="string",JSONPath=".status.password.origin"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// CredentialTemplate is one account of an application, rendered by the kind's
// chart. A controller keeps its account Secret, named <name>.account, and the
// Cozystack API mints and revokes through it. See
// design-proposals/user-secrets-api in cozystack/community.
type CredentialTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CredentialTemplateSpec   `json:"spec,omitempty"`
	Status CredentialTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CredentialTemplateList contains a list of CredentialTemplate.
type CredentialTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CredentialTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CredentialTemplate{}, &CredentialTemplateList{})
}
