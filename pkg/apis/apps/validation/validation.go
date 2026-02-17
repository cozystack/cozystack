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

package validation

import (
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateApplicationName validates that an Application name conforms to DNS-1035.
// This is required because Application names are used to create Kubernetes resources
// (Services, Namespaces, etc.) that must have DNS-1035 compliant names.
// Note: length validation is handled separately by validateNameLength in the REST
// handler, which computes dynamic limits based on Helm release prefix.
func ValidateApplicationName(name string, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if len(name) == 0 {
		allErrs = append(allErrs, field.Required(fldPath, "name is required"))
		return allErrs
	}

	for _, msg := range k8svalidation.IsDNS1035Label(name) {
		allErrs = append(allErrs, field.Invalid(fldPath, name, msg))
	}

	return allErrs
}
