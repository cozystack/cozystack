/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package application

import (
	"bytes"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const vmDiskKind = "VMDisk"

// storageOf reads storage from values, schema default applied, or reports
// false when it is absent or not a quantity. The schema admits a string or a
// number; numbers are decoded as written, since a byte count above 2^53 does
// not survive a float.
func (r *REST) storageOf(raw []byte) (resource.Quantity, bool) {
	values := map[string]any{}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&values); err != nil {
			return resource.Quantity{}, false
		}
	}
	if r.specSchema != nil {
		if err := defaulting.Apply(&values, r.specSchema); err != nil {
			return resource.Quantity{}, false
		}
	}
	var text string
	switch v := values["storage"].(type) {
	case string:
		text = v
	case json.Number:
		text = v.String()
	default:
		return resource.Quantity{}, false
	}
	q, err := resource.ParseQuantity(text)
	if err != nil {
		return resource.Quantity{}, false
	}
	return q, true
}

// validateVMDiskStorageNotLowered refuses lowering a disk's size. The chart
// only ever grows the volume and ignores a smaller value, so accepting one
// would store a size the disk does not have, and the tenant quota reservation
// would release capacity the volume still holds.
func (r *REST) validateVMDiskStorageNotLowered(old, app *appsv1alpha1.Application) field.ErrorList {
	var raw []byte
	if old.Spec != nil {
		raw = old.Spec.Raw
	}
	return r.validateVMDiskStorageNotLoweredFrom(raw, app)
}

// validateVMDiskStorageNotLoweredFrom is validateVMDiskStorageNotLowered
// against stored values, for a conflict retry that writes over a release
// another writer may have grown since.
func (r *REST) validateVMDiskStorageNotLoweredFrom(oldValues []byte, app *appsv1alpha1.Application) field.ErrorList {
	if r.kindName != vmDiskKind {
		return nil
	}
	before, ok := r.storageOf(oldValues)
	if !ok {
		return nil
	}
	var raw []byte
	if app.Spec != nil {
		raw = app.Spec.Raw
	}
	after, ok := r.storageOf(raw)
	if !ok || after.Cmp(before) >= 0 {
		return nil
	}
	return field.ErrorList{field.Forbidden(field.NewPath("spec", "storage"),
		fmt.Sprintf("a disk cannot be shrunk: it is %s and cannot be lowered to %s", before.String(), after.String()))}
}
