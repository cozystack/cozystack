// SPDX-License-Identifier: Apache-2.0

// Hand-written DeepCopy methods. The package opted not to take the full
// mariadb-operator Go API as a dependency, so deepcopy-gen is not wired up;
// the surface area is small enough to maintain by hand.

package mariadbtypes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ---------------------------------------------------------------------------
// MariaDB
// ---------------------------------------------------------------------------

func (in *MariaDB) DeepCopyInto(out *MariaDB) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *MariaDB) DeepCopy() *MariaDB {
	if in == nil {
		return nil
	}
	out := new(MariaDB)
	in.DeepCopyInto(out)
	return out
}

func (in *MariaDB) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *MariaDBList) DeepCopyInto(out *MariaDBList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]MariaDB, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *MariaDBList) DeepCopy() *MariaDBList {
	if in == nil {
		return nil
	}
	out := new(MariaDBList)
	in.DeepCopyInto(out)
	return out
}

func (in *MariaDBList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *MariaDBSpec) DeepCopyInto(out *MariaDBSpec) {
	*out = *in
	if in.BootstrapFrom != nil {
		in, out := &in.BootstrapFrom, &out.BootstrapFrom
		*out = new(BootstrapFrom)
		(*in).DeepCopyInto(*out)
	}
}

func (in *MariaDBSpec) DeepCopy() *MariaDBSpec {
	if in == nil {
		return nil
	}
	out := new(MariaDBSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *MariaDBStatus) DeepCopyInto(out *MariaDBStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *MariaDBStatus) DeepCopy() *MariaDBStatus {
	if in == nil {
		return nil
	}
	out := new(MariaDBStatus)
	in.DeepCopyInto(out)
	return out
}

func (in *BootstrapFrom) DeepCopyInto(out *BootstrapFrom) {
	*out = *in
	if in.BackupRef != nil {
		out.BackupRef = new(BackupReference)
		*out.BackupRef = *in.BackupRef
	}
}

func (in *BootstrapFrom) DeepCopy() *BootstrapFrom {
	if in == nil {
		return nil
	}
	out := new(BootstrapFrom)
	in.DeepCopyInto(out)
	return out
}

// ---------------------------------------------------------------------------
// Backup
// ---------------------------------------------------------------------------

func (in *Backup) DeepCopyInto(out *Backup) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
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

func (in *BackupSpec) DeepCopyInto(out *BackupSpec) {
	*out = *in
	out.MariaDBRef = in.MariaDBRef
	in.Storage.DeepCopyInto(&out.Storage)
	if in.Databases != nil {
		out.Databases = append([]string(nil), in.Databases...)
	}
	if in.MaxRetention != nil {
		out.MaxRetention = new(metav1.Duration)
		*out.MaxRetention = *in.MaxRetention
	}
}

func (in *BackupSpec) DeepCopy() *BackupSpec {
	if in == nil {
		return nil
	}
	out := new(BackupSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *BackupStatus) DeepCopyInto(out *BackupStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *BackupStatus) DeepCopy() *BackupStatus {
	if in == nil {
		return nil
	}
	out := new(BackupStatus)
	in.DeepCopyInto(out)
	return out
}

func (in *BackupStorage) DeepCopyInto(out *BackupStorage) {
	*out = *in
	if in.S3 != nil {
		out.S3 = new(S3Storage)
		in.S3.DeepCopyInto(out.S3)
	}
	if in.PersistentVolumeClaim != nil {
		out.PersistentVolumeClaim = in.PersistentVolumeClaim.DeepCopy()
	}
	if in.Volume != nil {
		out.Volume = in.Volume.DeepCopy()
	}
}

func (in *BackupStorage) DeepCopy() *BackupStorage {
	if in == nil {
		return nil
	}
	out := new(BackupStorage)
	in.DeepCopyInto(out)
	return out
}

func (in *S3Storage) DeepCopyInto(out *S3Storage) {
	*out = *in
	out.AccessKeyIdSecretKeyRef = in.AccessKeyIdSecretKeyRef
	out.SecretAccessKeySecretKeyRef = in.SecretAccessKeySecretKeyRef
	if in.SessionTokenSecretKeyRef != nil {
		out.SessionTokenSecretKeyRef = new(SecretKeySelector)
		*out.SessionTokenSecretKeyRef = *in.SessionTokenSecretKeyRef
	}
	if in.TLS != nil {
		out.TLS = new(S3TLS)
		in.TLS.DeepCopyInto(out.TLS)
	}
}

func (in *S3Storage) DeepCopy() *S3Storage {
	if in == nil {
		return nil
	}
	out := new(S3Storage)
	in.DeepCopyInto(out)
	return out
}

func (in *S3TLS) DeepCopyInto(out *S3TLS) {
	*out = *in
	if in.CASecretKeyRef != nil {
		out.CASecretKeyRef = new(SecretKeySelector)
		*out.CASecretKeyRef = *in.CASecretKeyRef
	}
}

func (in *S3TLS) DeepCopy() *S3TLS {
	if in == nil {
		return nil
	}
	out := new(S3TLS)
	in.DeepCopyInto(out)
	return out
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

func (in *Restore) DeepCopyInto(out *Restore) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *Restore) DeepCopy() *Restore {
	if in == nil {
		return nil
	}
	out := new(Restore)
	in.DeepCopyInto(out)
	return out
}

func (in *Restore) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *RestoreList) DeepCopyInto(out *RestoreList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Restore, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *RestoreList) DeepCopy() *RestoreList {
	if in == nil {
		return nil
	}
	out := new(RestoreList)
	in.DeepCopyInto(out)
	return out
}

func (in *RestoreList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *RestoreSpec) DeepCopyInto(out *RestoreSpec) {
	*out = *in
	out.MariaDBRef = in.MariaDBRef
	if in.BackupRef != nil {
		out.BackupRef = new(BackupReference)
		*out.BackupRef = *in.BackupRef
	}
	if in.Databases != nil {
		out.Databases = append([]string(nil), in.Databases...)
	}
}

func (in *RestoreSpec) DeepCopy() *RestoreSpec {
	if in == nil {
		return nil
	}
	out := new(RestoreSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *RestoreStatus) DeepCopyInto(out *RestoreStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *RestoreStatus) DeepCopy() *RestoreStatus {
	if in == nil {
		return nil
	}
	out := new(RestoreStatus)
	in.DeepCopyInto(out)
	return out
}
