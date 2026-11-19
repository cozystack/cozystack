// SPDX-License-Identifier: Apache-2.0

// Hand-written DeepCopy methods. The package opted not to take the full
// fdb-kubernetes-operator Go API as a dependency, so deepcopy-gen is not
// wired up; the surface area is small enough to maintain by hand.

package foundationdbtypes

import (
	"k8s.io/apimachinery/pkg/runtime"
)

// ---------------------------------------------------------------------------
// FoundationDBCluster
// ---------------------------------------------------------------------------

func (in *FoundationDBCluster) DeepCopyInto(out *FoundationDBCluster) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	out.Status = in.Status
}

func (in *FoundationDBCluster) DeepCopy() *FoundationDBCluster {
	if in == nil {
		return nil
	}
	out := new(FoundationDBCluster)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDBCluster) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FoundationDBClusterList) DeepCopyInto(out *FoundationDBClusterList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FoundationDBCluster, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *FoundationDBClusterList) DeepCopy() *FoundationDBClusterList {
	if in == nil {
		return nil
	}
	out := new(FoundationDBClusterList)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDBClusterList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// ---------------------------------------------------------------------------
// FoundationDBBackup
// ---------------------------------------------------------------------------

func (in *FoundationDBBackup) DeepCopyInto(out *FoundationDBBackup) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *FoundationDBBackup) DeepCopy() *FoundationDBBackup {
	if in == nil {
		return nil
	}
	out := new(FoundationDBBackup)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDBBackup) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FoundationDBBackupList) DeepCopyInto(out *FoundationDBBackupList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FoundationDBBackup, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *FoundationDBBackupList) DeepCopy() *FoundationDBBackupList {
	if in == nil {
		return nil
	}
	out := new(FoundationDBBackupList)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDBBackupList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FoundationDBBackupSpec) DeepCopyInto(out *FoundationDBBackupSpec) {
	*out = *in
	in.BlobStoreConfiguration.DeepCopyInto(&out.BlobStoreConfiguration)
	if in.AgentCount != nil {
		out.AgentCount = new(int32)
		*out.AgentCount = *in.AgentCount
	}
	if in.SnapshotPeriodSeconds != nil {
		out.SnapshotPeriodSeconds = new(int32)
		*out.SnapshotPeriodSeconds = *in.SnapshotPeriodSeconds
	}
	if in.CustomParameters != nil {
		out.CustomParameters = append([]string(nil), in.CustomParameters...)
	}
	if in.BackupDeploymentSpec != nil {
		out.BackupDeploymentSpec = new(BackupDeploymentSpec)
		in.BackupDeploymentSpec.DeepCopyInto(out.BackupDeploymentSpec)
	}
}

func (in *FoundationDBBackupSpec) DeepCopy() *FoundationDBBackupSpec {
	if in == nil {
		return nil
	}
	out := new(FoundationDBBackupSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDBBackupStatus) DeepCopyInto(out *FoundationDBBackupStatus) {
	*out = *in
	if in.BackupDetails != nil {
		out.BackupDetails = new(BackupDetails)
		*out.BackupDetails = *in.BackupDetails
	}
	if in.Generations != nil {
		out.Generations = new(BackupGenerationStatus)
		*out.Generations = *in.Generations
	}
}

func (in *FoundationDBBackupStatus) DeepCopy() *FoundationDBBackupStatus {
	if in == nil {
		return nil
	}
	out := new(FoundationDBBackupStatus)
	in.DeepCopyInto(out)
	return out
}

func (in *BackupDeploymentSpec) DeepCopyInto(out *BackupDeploymentSpec) {
	*out = *in
	if in.PodTemplateSpec != nil {
		out.PodTemplateSpec = in.PodTemplateSpec.DeepCopy()
	}
}

func (in *BlobStoreConfiguration) DeepCopyInto(out *BlobStoreConfiguration) {
	*out = *in
	if in.URLParameters != nil {
		out.URLParameters = append([]string(nil), in.URLParameters...)
	}
}

// ---------------------------------------------------------------------------
// FoundationDBRestore
// ---------------------------------------------------------------------------

func (in *FoundationDBRestore) DeepCopyInto(out *FoundationDBRestore) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

func (in *FoundationDBRestore) DeepCopy() *FoundationDBRestore {
	if in == nil {
		return nil
	}
	out := new(FoundationDBRestore)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDBRestore) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FoundationDBRestoreList) DeepCopyInto(out *FoundationDBRestoreList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FoundationDBRestore, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *FoundationDBRestoreList) DeepCopy() *FoundationDBRestoreList {
	if in == nil {
		return nil
	}
	out := new(FoundationDBRestoreList)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDBRestoreList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FoundationDBRestoreSpec) DeepCopyInto(out *FoundationDBRestoreSpec) {
	*out = *in
	in.BlobStoreConfiguration.DeepCopyInto(&out.BlobStoreConfiguration)
	if in.CustomParameters != nil {
		out.CustomParameters = append([]string(nil), in.CustomParameters...)
	}
	if in.KeyRanges != nil {
		out.KeyRanges = append([]KeyRange(nil), in.KeyRanges...)
	}
}
