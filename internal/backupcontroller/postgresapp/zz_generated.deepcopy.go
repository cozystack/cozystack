// SPDX-License-Identifier: Apache-2.0

// Hand-written DeepCopy methods. The package opted not to wire deepcopy-gen
// (this is an internal partial-schema mirror, not a generated public API);
// the surface area is small enough to maintain by hand.

package postgresapp

import (
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *Postgres) DeepCopyInto(out *Postgres) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

func (in *Postgres) DeepCopy() *Postgres {
	if in == nil {
		return nil
	}
	out := new(Postgres)
	in.DeepCopyInto(out)
	return out
}

func (in *Postgres) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PostgresList) DeepCopyInto(out *PostgresList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Postgres, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PostgresList) DeepCopy() *PostgresList {
	if in == nil {
		return nil
	}
	out := new(PostgresList)
	in.DeepCopyInto(out)
	return out
}

func (in *PostgresList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *PostgresSpec) DeepCopyInto(out *PostgresSpec) {
	*out = *in
	out.Bootstrap = in.Bootstrap
	out.Backup = in.Backup
	if in.Databases != nil {
		out.Databases = make(map[string]Database, len(in.Databases))
		for k, v := range in.Databases {
			out.Databases[k] = *v.DeepCopy()
		}
	}
	if in.Users != nil {
		out.Users = make(map[string]User, len(in.Users))
		for k, v := range in.Users {
			out.Users[k] = v
		}
	}
}

func (in *Database) DeepCopyInto(out *Database) {
	*out = *in
	in.Roles.DeepCopyInto(&out.Roles)
	if in.Extensions != nil {
		out.Extensions = make([]string, len(in.Extensions))
		copy(out.Extensions, in.Extensions)
	}
}

func (in *Database) DeepCopy() *Database {
	if in == nil {
		return nil
	}
	out := new(Database)
	in.DeepCopyInto(out)
	return out
}

func (in *DatabaseRoles) DeepCopyInto(out *DatabaseRoles) {
	*out = *in
	if in.Admin != nil {
		out.Admin = make([]string, len(in.Admin))
		copy(out.Admin, in.Admin)
	}
	if in.Readonly != nil {
		out.Readonly = make([]string, len(in.Readonly))
		copy(out.Readonly, in.Readonly)
	}
}
