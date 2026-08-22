// SPDX-License-Identifier: Apache-2.0

// Hand-written DeepCopy methods. As with mariadbtypes, this package opted not
// to take the full percona-server-mongodb-operator Go API as a dependency, so
// deepcopy-gen is not wired up; the surface area is small enough to maintain
// by hand.

package psmdbtypes

import (
	"k8s.io/apimachinery/pkg/runtime"
)

// ---------------------------------------------------------------------------
// PerconaServerMongoDB
// ---------------------------------------------------------------------------

func (in *PerconaServerMongoDB) DeepCopyInto(out *PerconaServerMongoDB) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

func (in *PerconaServerMongoDB) DeepCopy() *PerconaServerMongoDB {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDB)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDB) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PerconaServerMongoDBList) DeepCopyInto(out *PerconaServerMongoDBList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]PerconaServerMongoDB, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PerconaServerMongoDBList) DeepCopy() *PerconaServerMongoDBList {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBList)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDBList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PerconaServerMongoDBSpec) DeepCopyInto(out *PerconaServerMongoDBSpec) {
	*out = *in
	in.Backup.DeepCopyInto(&out.Backup)
}

func (in *PerconaServerMongoDBSpec) DeepCopy() *PerconaServerMongoDBSpec {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDBBackupConfig) DeepCopyInto(out *PerconaServerMongoDBBackupConfig) {
	*out = *in
	if in.Storages != nil {
		out.Storages = make(map[string]runtime.RawExtension, len(in.Storages))
		for k, v := range in.Storages {
			out.Storages[k] = *v.DeepCopy()
		}
	}
}

func (in *PerconaServerMongoDBBackupConfig) DeepCopy() *PerconaServerMongoDBBackupConfig {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBBackupConfig)
	in.DeepCopyInto(out)
	return out
}

// ---------------------------------------------------------------------------
// PerconaServerMongoDBBackup
// ---------------------------------------------------------------------------

func (in *PerconaServerMongoDBBackup) DeepCopyInto(out *PerconaServerMongoDBBackup) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *PerconaServerMongoDBBackup) DeepCopy() *PerconaServerMongoDBBackup {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBBackup)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDBBackup) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PerconaServerMongoDBBackupList) DeepCopyInto(out *PerconaServerMongoDBBackupList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]PerconaServerMongoDBBackup, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PerconaServerMongoDBBackupList) DeepCopy() *PerconaServerMongoDBBackupList {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBBackupList)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDBBackupList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PerconaServerMongoDBBackupSpec) DeepCopyInto(out *PerconaServerMongoDBBackupSpec) {
	*out = *in
	if in.CompressionLevel != nil {
		out.CompressionLevel = new(int)
		*out.CompressionLevel = *in.CompressionLevel
	}
}

func (in *PerconaServerMongoDBBackupSpec) DeepCopy() *PerconaServerMongoDBBackupSpec {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBBackupSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDBBackupStatus) DeepCopyInto(out *PerconaServerMongoDBBackupStatus) {
	*out = *in
	if in.Completed != nil {
		out.Completed = in.Completed.DeepCopy()
	}
	if in.LastTransition != nil {
		out.LastTransition = in.LastTransition.DeepCopy()
	}
	if in.LatestRestorableTime != nil {
		out.LatestRestorableTime = in.LatestRestorableTime.DeepCopy()
	}
	if in.S3 != nil {
		out.S3 = new(BackupStorageS3)
		in.S3.DeepCopyInto(out.S3)
	}
}

func (in *PerconaServerMongoDBBackupStatus) DeepCopy() *PerconaServerMongoDBBackupStatus {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBBackupStatus)
	in.DeepCopyInto(out)
	return out
}

// ---------------------------------------------------------------------------
// PerconaServerMongoDBRestore
// ---------------------------------------------------------------------------

func (in *PerconaServerMongoDBRestore) DeepCopyInto(out *PerconaServerMongoDBRestore) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *PerconaServerMongoDBRestore) DeepCopy() *PerconaServerMongoDBRestore {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBRestore)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDBRestore) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PerconaServerMongoDBRestoreList) DeepCopyInto(out *PerconaServerMongoDBRestoreList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]PerconaServerMongoDBRestore, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PerconaServerMongoDBRestoreList) DeepCopy() *PerconaServerMongoDBRestoreList {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBRestoreList)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDBRestoreList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PerconaServerMongoDBRestoreSpec) DeepCopyInto(out *PerconaServerMongoDBRestoreSpec) {
	*out = *in
	if in.BackupSource != nil {
		out.BackupSource = new(BackupSource)
		in.BackupSource.DeepCopyInto(out.BackupSource)
	}
	if in.PITR != nil {
		out.PITR = new(PITRSpec)
		*out.PITR = *in.PITR
	}
}

func (in *PerconaServerMongoDBRestoreSpec) DeepCopy() *PerconaServerMongoDBRestoreSpec {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBRestoreSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *PerconaServerMongoDBRestoreStatus) DeepCopyInto(out *PerconaServerMongoDBRestoreStatus) {
	*out = *in
	if in.Completed != nil {
		out.Completed = in.Completed.DeepCopy()
	}
}

func (in *PerconaServerMongoDBRestoreStatus) DeepCopy() *PerconaServerMongoDBRestoreStatus {
	if in == nil {
		return nil
	}
	out := new(PerconaServerMongoDBRestoreStatus)
	in.DeepCopyInto(out)
	return out
}

// ---------------------------------------------------------------------------
// Shared storage / PITR shapes
// ---------------------------------------------------------------------------

func (in *BackupSource) DeepCopyInto(out *BackupSource) {
	*out = *in
	if in.S3 != nil {
		out.S3 = new(BackupStorageS3)
		in.S3.DeepCopyInto(out.S3)
	}
}

func (in *BackupSource) DeepCopy() *BackupSource {
	if in == nil {
		return nil
	}
	out := new(BackupSource)
	in.DeepCopyInto(out)
	return out
}

func (in *BackupStorageS3) DeepCopyInto(out *BackupStorageS3) {
	*out = *in
	if in.ForcePathStyle != nil {
		out.ForcePathStyle = new(bool)
		*out.ForcePathStyle = *in.ForcePathStyle
	}
}

func (in *BackupStorageS3) DeepCopy() *BackupStorageS3 {
	if in == nil {
		return nil
	}
	out := new(BackupStorageS3)
	in.DeepCopyInto(out)
	return out
}

func (in *PITRSpec) DeepCopyInto(out *PITRSpec) {
	*out = *in
}

func (in *PITRSpec) DeepCopy() *PITRSpec {
	if in == nil {
		return nil
	}
	out := new(PITRSpec)
	in.DeepCopyInto(out)
	return out
}
