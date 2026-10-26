/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
)

// ApplicationDefinitionCredentials declares the accounts of an application
// kind and how the Cozystack API stores their passwords. Its presence converts
// the kind to platform-minted credentials. See
// design-proposals/user-secrets-api in cozystack/community.
type ApplicationDefinitionCredentials struct {
	// Format is how a password is stored in the account Secret: the verifier the
	// engine checks a login against, or the plaintext for an engine that needs it.
	Format CredentialFormat `json:"format"`

	// SecretLabels are added to every account Secret of the kind, for an operator
	// that notices a changed Secret only when it carries a label.
	// +optional
	SecretLabels map[string]string `json:"secretLabels,omitempty"`

	// ValuesPath makes the HelmRelease pull each tenant account's stored value
	// into this values key, under u<sha256 hex of the user name>, for a chart
	// whose operator cannot read a Secret. It starts with "_", which the
	// Application API hides on read and refuses on write, so the value never
	// appears in the application.
	// +optional
	// +kubebuilder:validation:Pattern=`^_[A-Za-z0-9_]*$`
	ValuesPath string `json:"valuesPath,omitempty"`

	// Accounts are the sources the kind's accounts come from. A kind may list
	// several, for example a values map of tenant users and a static platform
	// account.
	// +kubebuilder:validation:MinItems=1
	Accounts []CredentialAccountSource `json:"accounts"`
}

// CredentialFormatKind says where the function that turns a password into
// its stored value lives.
// +kubebuilder:validation:Enum=BuiltIn;Webhook
type CredentialFormatKind string

const (
	// CredentialFormatBuiltIn names a function compiled into the API.
	CredentialFormatBuiltIn CredentialFormatKind = "BuiltIn"
	// CredentialFormatWebhook names an endpoint the API calls with the
	// plaintext. Declared, not served yet.
	CredentialFormatWebhook CredentialFormatKind = "Webhook"
)

// CredentialFormat selects the stored form of a password.
type CredentialFormat struct {
	// Name of the format. The built-in names are scram-sha-256, mysql-native,
	// sha256 and plaintext; a Webhook format carries any name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind defaults to BuiltIn.
	// +optional
	Kind CredentialFormatKind `json:"kind,omitempty"`

	// Webhook is the endpoint of a Webhook format.
	// +optional
	Webhook *CredentialWebhook `json:"webhook,omitempty"`
}

// CredentialWebhook is the client configuration of a credentials webhook, in
// the shape every Kubernetes webhook uses.
type CredentialWebhook struct {
	ClientConfig admissionregistrationv1.WebhookClientConfig `json:"clientConfig"`

	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=30
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// CredentialAccountSourceKind says how a source yields account names.
// +kubebuilder:validation:Enum=ValuesMap;ValuesList;Static;Expression;Webhook
type CredentialAccountSourceKind string

const (
	// CredentialAccountsValuesMap takes the keys of a values map as accounts.
	CredentialAccountsValuesMap CredentialAccountSourceKind = "ValuesMap"
	// CredentialAccountsValuesList takes the name field of each object in a
	// values list as an account. Declared, not served yet.
	CredentialAccountsValuesList CredentialAccountSourceKind = "ValuesList"
	// CredentialAccountsStatic is one fixed account.
	CredentialAccountsStatic CredentialAccountSourceKind = "Static"
	// CredentialAccountsExpression evaluates a CEL expression over the
	// application. Declared, not served yet.
	CredentialAccountsExpression CredentialAccountSourceKind = "Expression"
	// CredentialAccountsWebhook asks an endpoint for the account list.
	// Declared, not served yet.
	CredentialAccountsWebhook CredentialAccountSourceKind = "Webhook"
)

// CredentialAccountOwner says whose account it is.
// +kubebuilder:validation:Enum=tenant;platform
type CredentialAccountOwner string

const (
	// CredentialAccountOwnerTenant is the default: the account gets a
	// Credential, an account Secret and a mint.
	CredentialAccountOwnerTenant CredentialAccountOwner = "tenant"
	// CredentialAccountOwnerPlatform marks an account the platform's own
	// components log in with. The API does nothing for it, and a ValuesMap
	// source skips its name.
	CredentialAccountOwnerPlatform CredentialAccountOwner = "platform"
)

// CredentialAccountSource is one source of a kind's accounts. Which fields
// apply depends on Kind.
type CredentialAccountSource struct {
	Kind CredentialAccountSourceKind `json:"kind"`

	// Path is the values key a ValuesMap or ValuesList source reads, as a
	// dotted path from the top of the application spec.
	// +optional
	Path string `json:"path,omitempty"`

	// NameField is the field of each object a ValuesList source takes the
	// account name from.
	// +optional
	NameField string `json:"nameField,omitempty"`

	// Name is the account of a Static source.
	// +optional
	Name string `json:"name,omitempty"`

	// When is a values key, as a dotted path, that switches a Static account
	// on when it holds true; absent, the account is always on.
	// +optional
	When string `json:"when,omitempty"`

	// Owner defaults to tenant.
	// +optional
	Owner CredentialAccountOwner `json:"owner,omitempty"`

	// Expression is the CEL expression of an Expression source.
	// +optional
	Expression string `json:"expression,omitempty"`

	// Webhook is the endpoint of a Webhook source.
	// +optional
	Webhook *CredentialWebhook `json:"webhook,omitempty"`
}
