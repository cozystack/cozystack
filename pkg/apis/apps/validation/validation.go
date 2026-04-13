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

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// TenantKind is the Application.Kind string that gates the tenant-specific
// name rules below. It must stay in sync with the `kind` field of the tenant
// ApplicationDefinition (packages/system/tenant-rd/cozyrds/tenant.yaml) which
// is the upstream source the aggregated API reads at startup via
// config.Application.Kind.
const TenantKind = "Tenant"

// tenantNameRegex enforces alphanumeric-only tenant names that begin with a
// lowercase letter. This is stricter than DNS-1035 because the tenant Helm
// chart's tenant.name helper (packages/apps/tenant/templates/_helpers.tpl)
// splits Release.Name on "-" and fails unless the result is exactly
// ["tenant", "<name>"]. Any dash inside <name> would break that invariant at
// Helm template time, so the aggregated API must reject such names up-front
// with a specific error. Requiring a leading letter (rather than letting
// leading-digit names fall through to DNS-1035) keeps the error message
// tenant-specific for all invalid inputs.
var tenantNameRegex = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// ValidateApplicationName validates that an Application name is acceptable for
// the given kind. All applications must conform to DNS-1035 because their
// names are used to create Kubernetes resources (Services, Namespaces, etc.)
// that require DNS-1035 compliance. Tenant applications additionally must be
// alphanumeric and begin with a lowercase letter because of the Helm chart
// constraint described on tenantNameRegex.
// Note: length validation is handled separately by validateNameLength in the
// REST handler, which computes dynamic limits based on Helm release prefix.
func ValidateApplicationName(name, kindName string, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if len(name) == 0 {
		allErrs = append(allErrs, field.Required(fldPath, "name is required"))
		return allErrs
	}

	// Tenant names must be alphanumeric starting with a letter — see
	// tenantNameRegex comment for the reason. Check before DNS-1035 so the
	// error message is specific to the tenant contract, not the generic DNS
	// label rules.
	if kindName == TenantKind && !tenantNameRegex.MatchString(name) {
		allErrs = append(allErrs, field.Invalid(fldPath, name,
			"tenant names must start with a lowercase letter and contain only lowercase letters and digits; dashes are not allowed"))
		return allErrs
	}

	for _, msg := range k8svalidation.IsDNS1035Label(name) {
		allErrs = append(allErrs, field.Invalid(fldPath, name, msg))
	}

	return allErrs
}
