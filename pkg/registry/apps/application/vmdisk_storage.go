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
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const vmDiskKind = "VMDisk"

// storageOf reads spec.storage, schema default applied, or reports false when
// it is absent or not a quantity.
func (r *REST) storageOf(app *appsv1alpha1.Application) (resource.Quantity, bool) {
	values := map[string]any{}
	if app.Spec != nil && len(app.Spec.Raw) > 0 {
		if err := json.Unmarshal(app.Spec.Raw, &values); err != nil {
			return resource.Quantity{}, false
		}
	}
	if r.specSchema != nil {
		if err := defaulting.Apply(&values, r.specSchema); err != nil {
			return resource.Quantity{}, false
		}
	}
	raw, ok := values["storage"].(string)
	if !ok {
		return resource.Quantity{}, false
	}
	q, err := resource.ParseQuantity(raw)
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
	if r.kindName != vmDiskKind {
		return nil
	}
	before, ok := r.storageOf(old)
	if !ok {
		return nil
	}
	after, ok := r.storageOf(app)
	if !ok || after.Cmp(before) >= 0 {
		return nil
	}
	return field.ErrorList{field.Forbidden(field.NewPath("spec", "storage"),
		fmt.Sprintf("a disk cannot be shrunk: it is %s and cannot be lowered to %s", before.String(), after.String()))}
}
