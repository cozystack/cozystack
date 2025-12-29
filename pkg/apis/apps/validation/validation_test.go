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
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateApplicationName(t *testing.T) {
	tests := []struct {
		name      string
		appName   string
		wantError bool
	}{
		// Valid names
		{"valid simple name", "tenant-one", false},
		{"valid single letter", "a", false},
		{"valid with numbers", "abc-123", false},
		{"valid lowercase", "my-tenant", false},
		{"valid long name", "my-very-long-tenant-name-123", false},

		// Invalid names
		{"starts with digit", "1john", true},
		{"only digits", "123", true},
		{"starts with hyphen", "-tenant", true},
		{"ends with hyphen", "tenant-", true},
		{"uppercase letters", "Tenant", true},
		{"mixed case", "myTenant", true},
		{"underscore", "my_tenant", true},
		{"dot", "my.tenant", true},
		{"empty string", "", true},
		{"space", "my tenant", true},
		{"too long (64 chars)", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"max length (63 chars)", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateApplicationName(tt.appName, field.NewPath("metadata").Child("name"))
			if (len(errs) > 0) != tt.wantError {
				t.Errorf("ValidateApplicationName(%q) returned %d errors, wantError = %v", tt.appName, len(errs), tt.wantError)
			}
		})
	}
}
