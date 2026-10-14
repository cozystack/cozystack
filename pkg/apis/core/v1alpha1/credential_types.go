// SPDX-License-Identifier: Apache-2.0

// Credential is the account record of one platform-generated credential of a
// managed application: who can know the live password and who changed it. It
// is a view over the account Secret the Cozystack API keeps per account, and
// it never carries the password or the stored verifier. The password is
// obtained once, through the mint subresource, and replaced through it. See
// design-proposals/user-secrets-api in cozystack/community.

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Credential is one account of a converted application.
type Credential struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CredentialSpec   `json:"spec,omitempty"`
	Status CredentialStatus `json:"status,omitempty"`
}

// CredentialSpec identifies the account. It is derived from the application
// and read-only.
type CredentialSpec struct {
	// Application is the managed application the account belongs to.
	Application CredentialApplicationRef `json:"application"`
	// User is the account name as the application declares it.
	User string `json:"user"`
}

// CredentialApplicationRef names a managed application.
type CredentialApplicationRef struct {
	APIGroup string `json:"apiGroup"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

// CredentialStatus is the account's record.
type CredentialStatus struct {
	// Password says where the live password came from.
	Password CredentialPasswordState `json:"password,omitempty"`
	// History holds the last ten operations, newest first.
	// +optional
	History []CredentialHistoryEntry `json:"history,omitempty"`
}

// CredentialOrigin says who can know the live password.
type CredentialOrigin string

const (
	// CredentialOriginNotIssued: the account was seeded with a random password
	// the API discarded at once. Nobody can log in until someone mints.
	CredentialOriginNotIssued CredentialOrigin = "NotIssued"
	// CredentialOriginMigrated: the account went through the conversion with
	// the password tenants could read before it; a mint should replace it.
	CredentialOriginMigrated CredentialOrigin = "Migrated"
	// CredentialOriginMinted: the password was shown once, to the caller of
	// the mint recorded in At and By.
	CredentialOriginMinted CredentialOrigin = "Minted"
	// CredentialOriginRevoked: the live password is random and discarded, so
	// nobody holds a working one until the next mint.
	CredentialOriginRevoked CredentialOrigin = "Revoked"
)

// CredentialPasswordState records the operation that set the live password.
type CredentialPasswordState struct {
	Origin CredentialOrigin `json:"origin,omitempty"`
	// +optional
	At metav1.Time `json:"at,omitempty"`
	// +optional
	By string `json:"by,omitempty"`
}

// CredentialOperation names a recorded operation on an account.
type CredentialOperation string

const (
	CredentialOperationCreate  CredentialOperation = "Create"
	CredentialOperationMigrate CredentialOperation = "Migrate"
	CredentialOperationMint    CredentialOperation = "Mint"
	CredentialOperationRevoke  CredentialOperation = "Revoke"
)

// CredentialHistoryEntry is one recorded operation.
type CredentialHistoryEntry struct {
	Operation CredentialOperation `json:"operation"`
	At        metav1.Time         `json:"at"`
	By        string              `json:"by"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CredentialList is a list of Credential objects.
type CredentialList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Credential `json:"items"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CredentialRequest is the body and the answer of the mint and revoke
// subresources of a Credential, in the shape of a Kubernetes TokenRequest: a
// create on the subresource carries the request in spec and is answered with
// the result in status, and nothing is stored. The password in a mint's answer
// is the only place it ever exists outside the API's memory.
type CredentialRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CredentialRequestSpec   `json:"spec,omitempty"`
	Status CredentialRequestStatus `json:"status,omitempty"`
}

// CredentialRequestSpec carries the caller's preconditions.
type CredentialRequestSpec struct {
	// +optional
	Preconditions *CredentialRequestPreconditions `json:"preconditions,omitempty"`
}

// CredentialRequestPreconditions makes the operation conditional on the
// Credential being unchanged since the caller read it, so a mint cannot
// silently replace a password someone else just received.
type CredentialRequestPreconditions struct {
	// ResourceVersion is the Credential's resourceVersion the caller last read.
	// +optional
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

// CredentialRequestStatus is the result of a mint or a revoke.
type CredentialRequestStatus struct {
	// Username is the account name as the engine knows it.
	Username string `json:"username,omitempty"`
	// Password is the new plaintext, present in a mint's answer only and
	// shown once.
	// +optional
	Password string `json:"password,omitempty"`
	// IssuedAt is when the operation took effect in the record.
	IssuedAt metav1.Time `json:"issuedAt,omitempty"`
}
