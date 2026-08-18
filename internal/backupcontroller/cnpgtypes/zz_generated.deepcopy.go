// SPDX-License-Identifier: Apache-2.0

// Hand-written DeepCopy methods. The package opted not to take the full
// CloudNativePG Go API as a dependency, so deepcopy-gen is not wired up;
// the surface area is small enough to maintain by hand.

package cnpgtypes

import (
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *Cluster) DeepCopyInto(out *Cluster) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

func (in *Cluster) DeepCopy() *Cluster {
	if in == nil {
		return nil
	}
	out := new(Cluster)
	in.DeepCopyInto(out)
	return out
}

func (in *Cluster) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ClusterList) DeepCopyInto(out *ClusterList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Cluster, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *ClusterList) DeepCopy() *ClusterList {
	if in == nil {
		return nil
	}
	out := new(ClusterList)
	in.DeepCopyInto(out)
	return out
}

func (in *ClusterList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ClusterSpec) DeepCopyInto(out *ClusterSpec) {
	*out = *in
	if in.Backup != nil {
		out.Backup = new(BackupConfiguration)
		in.Backup.DeepCopyInto(out.Backup)
	}
	if in.Bootstrap != nil {
		out.Bootstrap = new(BootstrapConfiguration)
		in.Bootstrap.DeepCopyInto(out.Bootstrap)
	}
	if in.Plugins != nil {
		out.Plugins = make([]PluginConfiguration, len(in.Plugins))
		for i := range in.Plugins {
			in.Plugins[i].DeepCopyInto(&out.Plugins[i])
		}
	}
}

func (in *PluginConfiguration) DeepCopyInto(out *PluginConfiguration) {
	*out = *in
	if in.IsWALArchiver != nil {
		out.IsWALArchiver = new(bool)
		*out.IsWALArchiver = *in.IsWALArchiver
	}
	if in.Parameters != nil {
		out.Parameters = make(map[string]string, len(in.Parameters))
		for k, v := range in.Parameters {
			out.Parameters[k] = v
		}
	}
}

func (in *BackupConfiguration) DeepCopyInto(out *BackupConfiguration) {
	*out = *in
	if in.BarmanObjectStore != nil {
		out.BarmanObjectStore = new(BarmanObjectStoreConfiguration)
		in.BarmanObjectStore.DeepCopyInto(out.BarmanObjectStore)
	}
}

func (in *BarmanObjectStoreConfiguration) DeepCopyInto(out *BarmanObjectStoreConfiguration) {
	*out = *in
	if in.EndpointCA != nil {
		out.EndpointCA = new(SecretKeySelector)
		*out.EndpointCA = *in.EndpointCA
	}
	if in.S3Credentials != nil {
		out.S3Credentials = new(S3Credentials)
		in.S3Credentials.DeepCopyInto(out.S3Credentials)
	}
	if in.Wal != nil {
		out.Wal = new(WalBackupConfiguration)
		*out.Wal = *in.Wal
	}
	if in.Data != nil {
		out.Data = new(DataBackupConfiguration)
		in.Data.DeepCopyInto(out.Data)
	}
}

func (in *S3Credentials) DeepCopyInto(out *S3Credentials) {
	*out = *in
	if in.AccessKeyID != nil {
		out.AccessKeyID = new(SecretKeySelector)
		*out.AccessKeyID = *in.AccessKeyID
	}
	if in.SecretAccessKey != nil {
		out.SecretAccessKey = new(SecretKeySelector)
		*out.SecretAccessKey = *in.SecretAccessKey
	}
}

func (in *DataBackupConfiguration) DeepCopyInto(out *DataBackupConfiguration) {
	*out = *in
	if in.Jobs != nil {
		out.Jobs = new(int32)
		*out.Jobs = *in.Jobs
	}
}

func (in *BootstrapConfiguration) DeepCopyInto(out *BootstrapConfiguration) {
	*out = *in
	if in.Recovery != nil {
		out.Recovery = new(RecoverySource)
		in.Recovery.DeepCopyInto(out.Recovery)
	}
}

func (in *RecoverySource) DeepCopyInto(out *RecoverySource) {
	*out = *in
	if in.RecoveryTarget != nil {
		out.RecoveryTarget = new(RecoveryTarget)
		*out.RecoveryTarget = *in.RecoveryTarget
	}
}

func (in *Backup) DeepCopyInto(out *Backup) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *BackupSpec) DeepCopyInto(out *BackupSpec) {
	*out = *in
	out.Cluster = in.Cluster
	if in.PluginConfiguration != nil {
		out.PluginConfiguration = new(BackupPluginConfiguration)
		in.PluginConfiguration.DeepCopyInto(out.PluginConfiguration)
	}
}

func (in *BackupPluginConfiguration) DeepCopyInto(out *BackupPluginConfiguration) {
	*out = *in
	if in.Parameters != nil {
		out.Parameters = make(map[string]string, len(in.Parameters))
		for k, v := range in.Parameters {
			out.Parameters[k] = v
		}
	}
}

func (in *ObjectStore) DeepCopyInto(out *ObjectStore) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

func (in *ObjectStore) DeepCopy() *ObjectStore {
	if in == nil {
		return nil
	}
	out := new(ObjectStore)
	in.DeepCopyInto(out)
	return out
}

func (in *ObjectStore) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ObjectStoreList) DeepCopyInto(out *ObjectStoreList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ObjectStore, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *ObjectStoreList) DeepCopy() *ObjectStoreList {
	if in == nil {
		return nil
	}
	out := new(ObjectStoreList)
	in.DeepCopyInto(out)
	return out
}

func (in *ObjectStoreList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ObjectStoreSpec) DeepCopyInto(out *ObjectStoreSpec) {
	*out = *in
	in.Configuration.DeepCopyInto(&out.Configuration)
	if in.InstanceSidecarConfiguration != nil {
		out.InstanceSidecarConfiguration = new(InstanceSidecarConfiguration)
		in.InstanceSidecarConfiguration.DeepCopyInto(out.InstanceSidecarConfiguration)
	}
}

func (in *InstanceSidecarConfiguration) DeepCopyInto(out *InstanceSidecarConfiguration) {
	*out = *in
	if in.Env != nil {
		out.Env = make([]EnvVar, len(in.Env))
		copy(out.Env, in.Env)
	}
}

func (in *InstanceSidecarConfiguration) DeepCopy() *InstanceSidecarConfiguration {
	if in == nil {
		return nil
	}
	out := new(InstanceSidecarConfiguration)
	in.DeepCopyInto(out)
	return out
}

func (in *Backup) DeepCopy() *Backup {
	if in == nil {
		return nil
	}
	out := new(Backup)
	in.DeepCopyInto(out)
	return out
}

func (in *Backup) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *BackupList) DeepCopyInto(out *BackupList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Backup, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *BackupList) DeepCopy() *BackupList {
	if in == nil {
		return nil
	}
	out := new(BackupList)
	in.DeepCopyInto(out)
	return out
}

func (in *BackupList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *BackupStatus) DeepCopyInto(out *BackupStatus) {
	*out = *in
	if in.StartedAt != nil {
		out.StartedAt = in.StartedAt.DeepCopy()
	}
}
