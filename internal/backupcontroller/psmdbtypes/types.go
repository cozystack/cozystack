// SPDX-License-Identifier: Apache-2.0

// Package psmdbtypes declares the minimum subset of psmdb.percona.com/v1 CRD
// shape that the backupstrategy controller operates on. As with mariadbtypes,
// we avoid pulling the full upstream percona-server-mongodb-operator Go API
// (which transitively imports a large surface area we do not need) while still
// letting us drop unstructured.Unstructured from the driver and its tests.
//
// The driver reads PerconaServerMongoDB for an existence/backup-enabled gate,
// creates PerconaServerMongoDBBackup CRs on the BackupJob path, and creates
// PerconaServerMongoDBRestore CRs on the RestoreJob path. It never patches the
// operator CRs, so the partial specs below only carry the fields the driver
// writes; unknown fields are preserved by the server on any merge patch.
//
// +groupName=psmdb.percona.com
// +versionName=v1
package psmdbtypes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "psmdb.percona.com"
	Version   = "v1"

	// Backup / Restore terminal + in-progress states. The psmdb operator
	// (percona-backup-mongodb underneath) expresses run state as a plain
	// string in .status.state rather than metav1 conditions, so the driver
	// matches on these literals. "ready" is the only success value; "error"
	// and "rejected" are terminal failures; the rest are in-progress
	// (including the empty string a freshly-created CR carries before the
	// operator picks it up).
	StateReady     = "ready"
	StateError     = "error"
	StateRejected  = "rejected"
	StateRequested = "requested"
	StateRunning   = "running"
	StateWaiting   = "waiting"

	// BackupTypeLogical is the only backup type the driver takes. Logical
	// (mongodump/pbm logical) dumps are portable across clusters, which is
	// what makes restore-to-differently-named instances possible; physical
	// backups are pinned to the source topology.
	BackupTypeLogical = "logical"
)

var (
	GroupVersion  = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&PerconaServerMongoDB{}, &PerconaServerMongoDBList{},
		&PerconaServerMongoDBBackup{}, &PerconaServerMongoDBBackupList{},
		&PerconaServerMongoDBRestore{}, &PerconaServerMongoDBRestoreList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// ---------------------------------------------------------------------------
// PerconaServerMongoDB (cluster) — existence / backup-enabled gate
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type PerconaServerMongoDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PerconaServerMongoDBSpec   `json:"spec,omitempty"`
	Status            PerconaServerMongoDBStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PerconaServerMongoDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PerconaServerMongoDB `json:"items"`
}

// PerconaServerMongoDBSpec carries only the backup sub-tree the driver reads
// to gate on "does this cluster have on-demand backups wired up". The psmdb
// operator only runs the percona-backup-mongodb agents (and therefore only
// services PerconaServerMongoDBBackup CRs) when spec.backup.enabled is true
// and at least one storage is declared; without that a Backup CR would sit in
// "waiting"/"error" forever. The driver reads these to surface a precise
// precondition message instead of a generic deadline hang.
type PerconaServerMongoDBSpec struct {
	Backup PerconaServerMongoDBBackupConfig `json:"backup,omitempty"`
}

// PerconaServerMongoDBBackupConfig mirrors psmdb .spec.backup for the two
// fields the driver inspects. Storages is a name→config map; the driver only
// needs the key set (which storage names exist), so the value is opaque.
type PerconaServerMongoDBBackupConfig struct {
	Enabled  bool                            `json:"enabled,omitempty"`
	Storages map[string]runtime.RawExtension `json:"storages,omitempty"`
}

type PerconaServerMongoDBStatus struct {
	State string `json:"state,omitempty"`
}

// ---------------------------------------------------------------------------
// PerconaServerMongoDBBackup (on-demand logical backup)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type PerconaServerMongoDBBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PerconaServerMongoDBBackupSpec   `json:"spec,omitempty"`
	Status            PerconaServerMongoDBBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PerconaServerMongoDBBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PerconaServerMongoDBBackup `json:"items"`
}

// PerconaServerMongoDBBackupSpec mirrors the on-demand backup spec. The
// storage is NOT inlined here (unlike mariadb): psmdb resolves StorageName
// against the source cluster's spec.backup.storages, so the storage config
// lives on the PerconaServerMongoDB CR and the Backup CR only names it.
type PerconaServerMongoDBBackupSpec struct {
	ClusterName      string `json:"clusterName"`
	StorageName      string `json:"storageName,omitempty"`
	Type             string `json:"type,omitempty"`
	CompressionType  string `json:"compressionType,omitempty"`
	CompressionLevel *int   `json:"compressionLevel,omitempty"`
}

// PerconaServerMongoDBBackupStatus mirrors the status fields the driver reads
// to (a) drive the poll loop (State) and (b) build a restore backupSource
// (Destination + S3). The operator populates S3 with the fully-resolved
// storage config (bucket, endpoint, credentialsSecret, prefix, ...) it used,
// which is exactly what a restore-to-differently-named needs.
type PerconaServerMongoDBBackupStatus struct {
	State                string           `json:"state,omitempty"`
	Destination          string           `json:"destination,omitempty"`
	StorageName          string           `json:"storageName,omitempty"`
	Type                 string           `json:"type,omitempty"`
	PBMName              string           `json:"pbmName,omitempty"`
	Error                string           `json:"error,omitempty"`
	Completed            *metav1.Time     `json:"completed,omitempty"`
	LastTransition       *metav1.Time     `json:"lastTransition,omitempty"`
	LatestRestorableTime *metav1.Time     `json:"latestRestorableTime,omitempty"`
	S3                   *BackupStorageS3 `json:"s3,omitempty"`
}

// ---------------------------------------------------------------------------
// PerconaServerMongoDBRestore (logical restore, optionally to a new cluster)
// ---------------------------------------------------------------------------

// +kubebuilder:object:root=true
type PerconaServerMongoDBRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PerconaServerMongoDBRestoreSpec   `json:"spec,omitempty"`
	Status            PerconaServerMongoDBRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PerconaServerMongoDBRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PerconaServerMongoDBRestore `json:"items"`
}

// PerconaServerMongoDBRestoreSpec mirrors the restore spec. The driver always
// drives BackupSource (not BackupName): BackupName binds a restore to a
// PerconaServerMongoDBBackup CR of the SAME cluster, which cannot express
// restore-into-a-differently-named-cluster; BackupSource carries the S3
// destination + credentials explicitly, so it works both in-place and to-copy
// and survives the source Backup CR being reaped.
type PerconaServerMongoDBRestoreSpec struct {
	ClusterName  string        `json:"clusterName"`
	BackupName   string        `json:"backupName,omitempty"`
	BackupSource *BackupSource `json:"backupSource,omitempty"`
	StorageName  string        `json:"storageName,omitempty"`
	PITR         *PITRSpec     `json:"pitr,omitempty"`
}

type PerconaServerMongoDBRestoreStatus struct {
	State      string       `json:"state,omitempty"`
	Error      string       `json:"error,omitempty"`
	PBMName    string       `json:"pbmName,omitempty"`
	PITRTarget string       `json:"pitrTarget,omitempty"`
	Completed  *metav1.Time `json:"completed,omitempty"`
}

// ---------------------------------------------------------------------------
// Shared storage / PITR shapes
// ---------------------------------------------------------------------------

// BackupSource mirrors restore.spec.backupSource and the backup .status shape
// it is copied from. Destination is the full backup URI (e.g.
// s3://bucket/prefix/2024-...); S3 carries the storage config the operator
// needs to reach it.
type BackupSource struct {
	Type        string           `json:"type,omitempty"`
	Destination string           `json:"destination,omitempty"`
	StorageName string           `json:"storageName,omitempty"`
	S3          *BackupStorageS3 `json:"s3,omitempty"`
}

// BackupStorageS3 mirrors the psmdb s3 storage block as it appears both in
// backup .status.s3 and restore .spec.backupSource.s3. CredentialsSecret is a
// Secret NAME in the CR's namespace (by reference — never a raw credential),
// which keeps any persisted snapshot free of secret material.
type BackupStorageS3 struct {
	Bucket                string `json:"bucket,omitempty"`
	CredentialsSecret     string `json:"credentialsSecret,omitempty"`
	EndpointURL           string `json:"endpointUrl,omitempty"`
	Prefix                string `json:"prefix,omitempty"`
	Region                string `json:"region,omitempty"`
	StorageClass          string `json:"storageClass,omitempty"`
	ForcePathStyle        *bool  `json:"forcePathStyle,omitempty"`
	InsecureSkipTLSVerify bool   `json:"insecureSkipTLSVerify,omitempty"`
}

// PITRSpec mirrors restore.spec.pitr. Type is "date" or "latest"; Date is
// required for "date" (format "YYYY-MM-DD HH:MM:SS", enforced by the operator
// CRD's XValidation).
type PITRSpec struct {
	Type string `json:"type,omitempty"`
	Date string `json:"date,omitempty"`
}
