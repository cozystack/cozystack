/*
Copyright 2024 The Cozystack Authors.

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
	"reflect"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

var minimumWorkloadMemory = resource.MustParse("1Mi")

func workloadValues(values *apiextv1.JSON) (map[string]any, error) {
	if values == nil || len(values.Raw) == 0 {
		return nil, nil
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(values.Raw))
	decoder.UseNumber()
	err := decoder.Decode(&result)
	return result, err
}

func (r *REST) validateWorkloadMemory(app *appsv1alpha1.Application, previous *apiextv1.JSON) error {
	if r.specSchema == nil || app.Spec == nil {
		return nil
	}
	values, err := workloadValues(app.Spec)
	if err != nil {
		return apierrors.NewBadRequest("invalid application spec")
	}
	old, err := workloadValues(previous)
	if err != nil {
		return apierrors.NewInternalError(fmt.Errorf("cannot decode stored application spec"))
	}
	if errs := changedWorkloadMemory(values, old, r.specSchema, field.NewPath("spec")); len(errs) > 0 {
		return apierrors.NewInvalid(r.gvk.GroupKind(), app.Name, errs)
	}
	return nil
}

// Only walk declared object fields. Arbitrary chart settings and tenant resourceQuotas
// can also contain keys named "memory", with different semantics.
func changedWorkloadMemory(values, old map[string]any, schema *structuralschema.Structural, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	for _, key := range sortedKeys(values) {
		child, declared := schema.Properties[key]
		current, ok := values[key].(map[string]any)
		if !declared || !ok {
			continue
		}
		previous, _ := old[key].(map[string]any)
		where := path.Child(key)
		_, preset := schema.Properties["resourcesPreset"]
		if key == "resources" && preset && child.Properties["memory"].XIntOrString && child.Properties["cpu"].XIntOrString {
			memory := current["memory"]
			if memory == nil || reflect.DeepEqual(memory, previous["memory"]) {
				continue
			}
			q, err := resource.ParseQuantity(fmt.Sprint(memory))
			if err != nil {
				errs = append(errs, field.Invalid(where.Child("memory"), memory, "expected a Kubernetes quantity"))
				continue
			}
			if q.Cmp(minimumWorkloadMemory) < 0 {
				prior, err := resource.ParseQuantity(fmt.Sprint(previous["memory"]))
				if err != nil || prior.Cmp(q) != 0 {
					errs = append(errs, field.Invalid(where.Child("memory"), memory, "must be at least 1Mi; use a unit such as 512Mi or 8Gi"))
				}
			}
			continue
		}
		errs = append(errs, changedWorkloadMemory(current, previous, &child, where)...)
	}
	return errs
}
