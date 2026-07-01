// SPDX-License-Identifier: Apache-2.0

// Package mariadbtypes declares the minimum subset of k8s.mariadb.com/v1alpha1
// CRD shape that the backupstrategy controller operates on. We avoid pulling
// the full upstream mariadb-operator Go API (which transitively imports a
// large surface area we do not need) while letting us drop
// unstructured.Unstructured from the controller and tests.
//
// MergeFrom-style patches preserve unknown fields. Get + Patch builds two
// values from the same partial schema; the JSON merge patch is computed
// between them, so it only contains the fields we know about. The API
// server applies that patch to the full stored object on the server side,
// leaving everything else untouched.
//
// +groupName=k8s.mariadb.com
// +versionName=v1alpha1
package mariadbtypes

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "k8s.mariadb.com"
	Version   = "v1alpha1"

	// Operator-side condition type set on Backup and Restore once the run
	// terminates. Status=True means success; Status=False with Reason
	// explains the failure or in-progress state.
	ConditionTypeComplete = "Complete"

	// Reason values the upstream operator emits on the Complete condition.
	// JobComplete  -> Status=True (run succeeded).
	// JobFailed    -> Status=False (terminal failure of the backing Job).
	// JobRunning   -> Status=False (still in progress).
	// JobSuspended -> Status=False (admin-suspended via spec).
	ConditionReasonJobComplete  = "JobComplete"
	ConditionReasonJobFailed    = "JobFailed"
	ConditionReasonJobRunning   = "JobRunning"
	ConditionReasonJobSuspended = "JobSuspended"

	// BackupKind is the kind reference used in Restore.spec.backupRef.
	BackupKind = "Backup"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&MariaDB{}, &MariaDBList{},
		&Backup{}, &BackupList{},
		&Restore{}, &RestoreList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// ---------------------------------------------------------------------------
// MariaDB
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type MariaDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MariaDBSpec   `json:"spec,omitempty"`
	Status            MariaDBStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MariaDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MariaDB `json:"items"`
}

// MariaDBSpec is the partial spec the driver reads or patches on a
// k8s.mariadb.com/MariaDB CR. The driver only needs the CR for existence
// checks today; it does not read or write any spec field. Kept as a named
// (currently empty) struct so MariaDB stays a regular kubebuilder object
// rather than a degenerate shell.
type MariaDBSpec struct{}

type MariaDBStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BackupReference points at a Backup CR in the same namespace as the
// Restore CR. Used by Restore.spec.backupRef.
type BackupReference struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
}

// ---------------------------------------------------------------------------
// Backup (logical, mariadb-dump)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BackupSpec   `json:"spec,omitempty"`
	Status            BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}

// BackupSpec mirrors k8s.mariadb.com/v1alpha1 Backup.spec for the fields
// the driver writes. Unknown fields are preserved by the server during
// merge patches; everything we don't set falls back to operator defaults.
type BackupSpec struct {
	MariaDBRef  MariaDBObjectRef `json:"mariaDbRef"`
	Storage     BackupStorage    `json:"storage"`
	Databases   []string         `json:"databases,omitempty"`
	Compression string           `json:"compression,omitempty"`
	MaxRetention *metav1.Duration `json:"maxRetention,omitempty"`
	LogLevel    string           `json:"logLevel,omitempty"`
}

// MariaDBObjectRef is a local reference to the source/target MariaDB CR.
// Mirrors the operator-side LocalObjectReference shape (the operator
// resolves it within the same namespace as the Backup/Restore CR).
type MariaDBObjectRef struct {
	Name string `json:"name"`
}

// BackupStorage mirrors Backup.spec.storage. Exactly one branch must be
// set; this is enforced by the operator-side OpenAPI schema.
type BackupStorage struct {
	S3                    *S3Storage                        `json:"s3,omitempty"`
	PersistentVolumeClaim *corev1.PersistentVolumeClaimSpec `json:"persistentVolumeClaim,omitempty"`
	Volume                *corev1.VolumeSource              `json:"volume,omitempty"`
}

// S3Storage mirrors Backup.spec.storage.s3. Operator-required fields:
// Bucket, Endpoint, AccessKeyIdSecretKeyRef, SecretAccessKeySecretKeyRef.
type S3Storage struct {
	Bucket                      string             `json:"bucket"`
	Endpoint                    string             `json:"endpoint"`
	Prefix                      string             `json:"prefix,omitempty"`
	Region                      string             `json:"region,omitempty"`
	AccessKeyIdSecretKeyRef     SecretKeySelector  `json:"accessKeyIdSecretKeyRef"`
	SecretAccessKeySecretKeyRef SecretKeySelector  `json:"secretAccessKeySecretKeyRef"`
	SessionTokenSecretKeyRef    *SecretKeySelector `json:"sessionTokenSecretKeyRef,omitempty"`
	TLS                         *S3TLS             `json:"tls,omitempty"`
}

// SecretKeySelector mirrors the operator-side SecretKeySelector. The
// underlying Secret must live in the same namespace as the Backup CR.
type SecretKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// S3TLS mirrors Backup.spec.storage.s3.tls.
type S3TLS struct {
	Enabled        bool               `json:"enabled,omitempty"`
	CASecretKeyRef *SecretKeySelector `json:"caSecretKeyRef,omitempty"`
}

// BackupStatus mirrors Backup.status. The operator expresses success
// via condition type "Complete" with status=True.
type BackupStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ---------------------------------------------------------------------------
// Restore (logical replay of a Backup)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type Restore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RestoreSpec   `json:"spec,omitempty"`
	Status            RestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Restore `json:"items"`
}

// RestoreSpec mirrors k8s.mariadb.com/v1alpha1 Restore.spec for the fields
// the driver writes. Either BackupRef or S3 may identify the source.
type RestoreSpec struct {
	MariaDBRef         MariaDBObjectRef `json:"mariaDbRef"`
	BackupRef          *BackupReference `json:"backupRef,omitempty"`
	Databases          []string         `json:"databases,omitempty"`
	TargetRecoveryTime string           `json:"targetRecoveryTime,omitempty"`
}

// RestoreStatus mirrors Restore.status. Same condition convention as
// Backup: type=Complete, Status=True on success.
type RestoreStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
