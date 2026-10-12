// SPDX-License-Identifier: Apache-2.0

// Hand-written DeepCopy methods. The package opted not to wire deepcopy-gen
// (this is an internal partial-schema mirror, not a generated public API);
// the surface area is small enough to maintain by hand.

package mongodbapp

import (
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *MongoDB) DeepCopyInto(out *MongoDB) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
}

func (in *MongoDB) DeepCopy() *MongoDB {
	if in == nil {
		return nil
	}
	out := new(MongoDB)
	in.DeepCopyInto(out)
	return out
}

func (in *MongoDB) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *MongoDBList) DeepCopyInto(out *MongoDBList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]MongoDB, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *MongoDBList) DeepCopy() *MongoDBList {
	if in == nil {
		return nil
	}
	out := new(MongoDBList)
	in.DeepCopyInto(out)
	return out
}

func (in *MongoDBList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
