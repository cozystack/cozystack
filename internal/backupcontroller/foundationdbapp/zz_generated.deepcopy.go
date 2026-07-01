// SPDX-License-Identifier: Apache-2.0

// Hand-written DeepCopy methods. The package opted not to wire deepcopy-gen
// (this is an internal partial-schema mirror, not a generated public API);
// the surface area is small enough to maintain by hand.

package foundationdbapp

import (
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *FoundationDB) DeepCopyInto(out *FoundationDB) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
}

func (in *FoundationDB) DeepCopy() *FoundationDB {
	if in == nil {
		return nil
	}
	out := new(FoundationDB)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDB) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FoundationDBList) DeepCopyInto(out *FoundationDBList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FoundationDB, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *FoundationDBList) DeepCopy() *FoundationDBList {
	if in == nil {
		return nil
	}
	out := new(FoundationDBList)
	in.DeepCopyInto(out)
	return out
}

func (in *FoundationDBList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
