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
	"regexp"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// dns1035LabelRegex validates DNS-1035 label format.
// DNS-1035 labels must start with a letter, contain only lowercase alphanumeric
// characters or hyphens, and end with an alphanumeric character.
var dns1035LabelRegex = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// maxApplicationNameLength is the maximum length of an Application name.
// This is set to 40 (not 63) to allow room for prefixes like "tenant-"
// and nested tenant namespaces (e.g., "tenant-parent-child").
const maxApplicationNameLength = 40

// ValidateApplicationName validates that an Application name conforms to DNS-1035.
// This is required because Application names are used to create Kubernetes resources
// (Services, Namespaces, etc.) that must have DNS-1035 compliant names.
func ValidateApplicationName(name string, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if len(name) == 0 {
		allErrs = append(allErrs, field.Required(fldPath, "name is required"))
		return allErrs
	}

	if len(name) > maxApplicationNameLength {
		allErrs = append(allErrs, field.TooLongMaxLength(fldPath, name, maxApplicationNameLength))
	}

	if !dns1035LabelRegex.MatchString(name) {
		allErrs = append(allErrs, field.Invalid(fldPath, name,
			"a DNS-1035 label must consist of lower case alphanumeric characters or '-', "+
				"start with an alphabetic character, and end with an alphanumeric character "+
				"(e.g. 'my-name', regex used for validation is '[a-z]([-a-z0-9]*[a-z0-9])?')"))
	}

	return allErrs
}
