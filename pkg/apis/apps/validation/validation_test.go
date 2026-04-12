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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateApplicationName(t *testing.T) {
	tests := []struct {
		name      string
		appName   string
		kindName  string
		wantError bool
	}{
		// Valid names (non-tenant kinds permit DNS-1035 including hyphens)
		{"valid simple name", "tenant-one", "MySQL", false},
		{"valid single letter", "a", "MySQL", false},
		{"valid with numbers", "abc-123", "MySQL", false},
		{"valid lowercase", "my-tenant", "MySQL", false},
		{"valid long name", "my-very-long-tenant-name", "MySQL", false},
		{"valid double hyphen", "my--tenant", "MySQL", false},
		{"valid at DNS-1035 max (63 chars)", strings.Repeat("a", 63), "MySQL", false},
		{"valid with empty kind", "my-db", "", false},

		// Invalid: starts with wrong character
		{"starts with digit", "1john", "MySQL", true},
		{"only digits", "123", "MySQL", true},
		{"starts with hyphen", "-tenant", "MySQL", true},

		// Invalid: ends with wrong character
		{"ends with hyphen", "tenant-", "MySQL", true},

		// Invalid: wrong characters
		{"uppercase letters", "Tenant", "MySQL", true},
		{"mixed case", "myTenant", "MySQL", true},
		{"underscore", "my_tenant", "MySQL", true},
		{"dot", "my.tenant", "MySQL", true},
		{"space", "my tenant", "MySQL", true},
		{"unicode cyrillic", "тенант", "MySQL", true},
		{"unicode emoji", "tenant🚀", "MySQL", true},
		{"special chars", "tenant@home", "MySQL", true},
		{"colon", "tenant:one", "MySQL", true},
		{"slash", "tenant/one", "MySQL", true},

		// Invalid: empty or whitespace
		{"empty string", "", "MySQL", true},
		{"only spaces", "   ", "MySQL", true},
		{"leading space", " tenant", "MySQL", true},
		{"trailing space", "tenant ", "MySQL", true},

		// Invalid: exceeds DNS-1035 max length (63)
		{"too long (64 chars)", strings.Repeat("a", 64), "MySQL", true},
		{"way too long (100 chars)", strings.Repeat("a", 100), "MySQL", true},

		// Tenant kind: stricter alphanumeric-only rule.
		// The tenant Helm chart's tenant.name helper (packages/apps/tenant/templates/_helpers.tpl)
		// splits Release.Name on "-" and fails unless the result is exactly
		// ["tenant", "<name>"]. Any dash inside <name> breaks that invariant, so
		// the aggregated API must reject tenant names containing dashes up-front
		// with a specific error — instead of letting Flux reconciliation fail later.
		{"tenant alphanumeric simple", "foo", "Tenant", false},
		{"tenant alphanumeric with digits", "foo123", "Tenant", false},
		{"tenant single char", "a", "Tenant", false},
		{"tenant single hyphen", "foo-bar", "Tenant", true},
		{"tenant leading hyphen", "-foo", "Tenant", true},
		{"tenant trailing hyphen", "foo-", "Tenant", true},
		{"tenant double hyphen", "foo--bar", "Tenant", true},
		{"tenant uppercase", "Foo", "Tenant", true},
		{"tenant underscore", "foo_bar", "Tenant", true},
		{"tenant empty", "", "Tenant", true},
		// Leading digit must be caught by the tenant-specific regex (not by
		// falling through to DNS-1035) so the error message reflects the
		// tenant contract — see TestValidateApplicationName_TenantErrorMessage.
		{"tenant leading digit", "123foo", "Tenant", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateApplicationName(tt.appName, tt.kindName, field.NewPath("metadata").Child("name"))
			if (len(errs) > 0) != tt.wantError {
				t.Errorf("ValidateApplicationName(%q, kind=%q) returned %d errors, wantError = %v, errors = %v",
					tt.appName, tt.kindName, len(errs), tt.wantError, errs)
			}
		})
	}
}

// TestValidateApplicationName_TenantErrorMessage pins the contract that when
// a tenant name is invalid, the returned error message is specific to the
// tenant naming rule — not the generic DNS-1035 message. Otherwise users get
// back "must start with an alphabetic character" or similar and have no way
// to know the constraint is tied to the tenant Helm chart.
func TestValidateApplicationName_TenantErrorMessage(t *testing.T) {
	// Every tenant-invalid name below must surface a tenant-specific error
	// message. In particular, "123foo" starts with a digit — the original
	// implementation let that fall through to DNS-1035 with a generic error;
	// the regex is tightened specifically so this case fails up-front.
	invalidTenantNames := []string{
		"foo-bar",  // dash
		"-foo",     // leading dash
		"foo-",     // trailing dash
		"foo--bar", // double dash
		"Foo",      // uppercase
		"foo_bar",  // underscore
		"foo.bar",  // dot
		"foo bar",  // space
		"123foo",   // leading digit — must not fall through to DNS-1035
	}

	const wantSubstring = "tenant names must"

	for _, name := range invalidTenantNames {
		t.Run(name, func(t *testing.T) {
			errs := ValidateApplicationName(name, "Tenant", field.NewPath("metadata").Child("name"))
			if len(errs) == 0 {
				t.Fatalf("expected error for tenant name %q, got none", name)
			}
			if !strings.Contains(errs[0].Detail, wantSubstring) {
				t.Errorf("tenant name %q: error detail = %q, want substring %q (generic DNS-1035 message is not tenant-specific)",
					name, errs[0].Detail, wantSubstring)
			}
		})
	}
}

// TestValidateApplicationName_TenantLengthFallthrough documents the one
// invalid-tenant case where the error message is intentionally NOT tenant-
// specific: when a name contains only valid tenant characters but exceeds
// the DNS-1035 63-char label limit, the length error comes from DNS-1035
// because length is not a tenant-specific constraint (every application
// kind is subject to the same Kubernetes label limit). REST.validateNameLength
// further tightens the limit using the Helm release prefix, so tenants cannot
// actually reach 64 characters end-to-end — this test only pins the package-
// level fallthrough so a future refactor does not accidentally promote the
// length error into tenant-specific wording.
func TestValidateApplicationName_TenantLengthFallthrough(t *testing.T) {
	name := strings.Repeat("a", 64) // valid tenant pattern, too long for DNS-1035

	errs := ValidateApplicationName(name, "Tenant", field.NewPath("metadata").Child("name"))
	if len(errs) == 0 {
		t.Fatalf("expected DNS-1035 length error for 64-char tenant name, got none")
	}
	// This error is the generic DNS-1035 one, NOT the tenant-specific message.
	if strings.Contains(errs[0].Detail, "tenant names must") {
		t.Errorf("64-char tenant name should surface the generic DNS-1035 error, got tenant-specific: %q", errs[0].Detail)
	}
	// Sanity check: the DNS-1035 error we do expect mentions length bounds.
	if !strings.Contains(errs[0].Detail, "63") {
		t.Errorf("expected DNS-1035 length hint in error detail, got: %q", errs[0].Detail)
	}
}
