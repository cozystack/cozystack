// SPDX-License-Identifier: Apache-2.0

// Package postgresapp declares the typed shape of the apps.cozystack.io/v1alpha1
// Postgres CR that the CNPG backup driver reads and patches. It carries only
// the fields the driver touches - bootstrap, backup, databases, users -
// because the live CR has many more fields (resources, quorum, postgresql
// parameters, ...) that we never look at and that round-tripping through
// this partial schema would otherwise silently drop.
//
// MergeFrom-style patches preserve the unknown fields. The Get + Patch path
// builds two values from the same partial schema; the JSON merge patch is
// computed between them, so it only contains the fields we know about.
// The API server applies that patch to the full stored object on the
// server side, leaving everything else untouched.
//
// Living in an internal package keeps this duplication out of the public
// api/apps/v1alpha1/postgresql module, which exists for external consumers
// (the cozystack-api server, in particular) and has its own release cadence.
//
// +groupName=apps.cozystack.io
// +versionName=v1alpha1
package postgresapp

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "apps.cozystack.io"
	Version   = "v1alpha1"
	Kind      = "Postgres"
	ListKind  = Kind + "List"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(Kind), &Postgres{})
	scheme.AddKnownTypeWithName(GroupVersion.WithKind(ListKind), &PostgresList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type Postgres struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PostgresSpec `json:"spec,omitempty"`
}

type PostgresList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Postgres `json:"items"`
}

// PostgresSpec is the partial subset of the Postgres CR spec that the CNPG
// backup driver reads or mutates. Add new fields here only when the driver
// genuinely needs them; everything else stays on the server, untouched by
// the controller's patches.
type PostgresSpec struct {
	Bootstrap Bootstrap           `json:"bootstrap,omitempty"`
	Backup    Backup              `json:"backup,omitempty"`
	Databases map[string]Database `json:"databases,omitempty"`
	Users     map[string]User     `json:"users,omitempty"`
}

type Bootstrap struct {
	Enabled      bool   `json:"enabled,omitempty"`
	OldName      string `json:"oldName,omitempty"`
	ServerName   string `json:"serverName,omitempty"`
	RecoveryTime string `json:"recoveryTime,omitempty"`
}

type Backup struct {
	DestinationPath     string              `json:"destinationPath,omitempty"`
	EndpointURL         string              `json:"endpointURL,omitempty"`
	S3AccessKey         string              `json:"s3AccessKey,omitempty"`
	S3SecretKey         string              `json:"s3SecretKey,omitempty"`
	S3CredentialsSecret S3CredentialsSecret `json:"s3CredentialsSecret,omitempty"`
	EndpointCA          EndpointCA          `json:"endpointCA,omitempty"`
}

type S3CredentialsSecret struct {
	Name               string `json:"name,omitempty"`
	AccessKeyIDKey     string `json:"accessKeyIDKey,omitempty"`
	SecretAccessKeyKey string `json:"secretAccessKeyKey,omitempty"`
}

// EndpointCA references a Secret holding the CA bundle Barman should trust
// when the S3 endpoint presents a self-signed certificate. Mirrors the
// chart's backup.endpointCA value.
type EndpointCA struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key,omitempty"`
}

type Database struct {
	Roles      DatabaseRoles `json:"roles,omitempty"`
	Extensions []string      `json:"extensions,omitempty"`
}

type DatabaseRoles struct {
	Admin    []string `json:"admin,omitempty"`
	Readonly []string `json:"readonly,omitempty"`
}

type User struct {
	Password    string `json:"password,omitempty"`
	Replication bool   `json:"replication,omitempty"`
}
