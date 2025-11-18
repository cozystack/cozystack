// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines backups.cozystack.io API types.
//
// Group: backups.cozystack.io
// Version: v1alpha1
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type StorageSpecType string

const (
	StorageSpecTypeEmpty  StorageSpecType = ""
	StorageSpecTypeS3     StorageSpecType = "s3"
	StorageSpecTypeBucket StorageSpecType = "bucket"
)

// +kubebuilder:object:root=true

// Storage describes where and how to store a backup.
type Storage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the location and credentials for a backup store.
	Spec StorageSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// StorageList holds a list of backup stores.
type StorageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Storage `json:"items"`
}

// StorageSpec specifies the type and location of the backup store,
// and references or provides access credentials if needed.
type StorageSpec struct {
	// Type is the type of the backup store. Supported values are
	// [`s3`,`bucket`]. Defaults to `bucket`.
	// +optional
	Type StorageSpecType `json:"type,omitempty"`

	// Bucket holds the configuration parameters needed to store a
	// backup in a Cozystack bucket in the current namespace.
	// +optional
	Bucket StorageSpecBucket `json:"bucket,omitempty"`

	// S3 holds the configuration parameters neede to store a backup in
	// any S3-compatible bucket.
	// +optional
	S3 StorageSpecS3 `json:"s3,omitempty"`
}

// StorageSpecBucket holds the configuration parameters needed to
// store a backup in a Cozystack bucket in the current namespace.
type StorageSpecBucket struct {
	// Name is the name of the bucket.
	Name string `json:"name"`

	// Prefix is the prefix for the backup tarball.
	Prefix string `json:"prefix"`

	// Format is a go-template style string for the name of the tarball stored.
	// Defaults to `{{ .apiGroup }}-{{ .kind }}-{{ .name }}-{{ now | unixEpoch }}.tar.gz`.
	// +optional
	Format string `json:"format"`
}

// StorageSpecS3 holds the configuration parameters needed to store a
// backup in any S3-compatible bucket.
type StorageSpecS3 struct {
	/* TODO: review the spec before pushing any specific solution
	EndpointURL string
	AWSAccessKeyId string
	AWSSecretAccessKey string
	BucketName string
	Region string
	Prefix string
	Format string
	*/
}
