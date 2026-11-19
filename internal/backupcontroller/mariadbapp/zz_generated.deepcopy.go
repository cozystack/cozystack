// SPDX-License-Identifier: Apache-2.0

// Hand-written DeepCopy methods. The package opted not to wire deepcopy-gen
// (this is an internal partial-schema mirror, not a generated public API);
// the surface area is small enough to maintain by hand.

package mariadbapp

import (
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *MariaDB) DeepCopyInto(out *MariaDB) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
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
