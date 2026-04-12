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
	"fmt"
	"strings"
	"testing"

	"github.com/cozystack/cozystack/pkg/config"
)

func TestValidateNameLength(t *testing.T) {
	tests := []struct {
		name      string
		kindName  string
		prefix    string
		appName   string
		wantError bool
	}{
		{
			name:      "short name passes",
			kindName:  "MySQL",
			prefix:    "mysql-",
			appName:   "mydb",
			wantError: false,
		},
		{
			name:      "at helm boundary passes",
			kindName:  "MySQL",
			prefix:    "mysql-",
			appName:   strings.Repeat("a", 53-len("mysql-")), // exactly 47 chars
			wantError: false,
		},
		{
			name:      "exceeding helm limit fails",
			kindName:  "MySQL",
			prefix:    "mysql-",
			appName:   strings.Repeat("a", 53-len("mysql-")+1), // 48 chars
			wantError: true,
		},
		{
			name:      "tenant within helm limit passes",
			kindName:  "Tenant",
			prefix:    "tenant-",
			appName:   strings.Repeat("a", 53-len("tenant-")), // 46 chars
			wantError: false,
		},
		{
			name:      "tenant exceeding helm limit fails",
			kindName:  "Tenant",
			prefix:    "tenant-",
			appName:   strings.Repeat("a", 53-len("tenant-")+1), // 47 chars
			wantError: true,
		},
		{
			name:      "prefix consuming all helm capacity returns config error",
			kindName:  "MySQL",
			prefix:    strings.Repeat("x", 53), // prefix == maxHelmReleaseName → helmMax = 0
			appName:   "a",
			wantError: true,
		},
		{
			name:      "prefix exceeding helm capacity returns config error",
			kindName:  "MySQL",
			prefix:    strings.Repeat("x", 60), // prefix > maxHelmReleaseName → helmMax < 0
			appName:   "a",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &REST{
				kindName: tt.kindName,
				releaseConfig: config.ReleaseConfig{
					Prefix: tt.prefix,
				},
			}

			errs := r.validateNameLength(tt.appName)

			if tt.wantError && len(errs) == 0 {
				t.Errorf("expected error for name %q (len=%d), got none", tt.appName, len(tt.appName))
			}
			if !tt.wantError && len(errs) > 0 {
				t.Errorf("unexpected error for name %q (len=%d): %v", tt.appName, len(tt.appName), errs)
			}
		})
	}
}

// TestValidateTenantNamespaceLength covers the check that the computed
// workload namespace for a Tenant application fits inside the 63-char
// DNS-1123 label limit. The namespace is formed by dash-joining the
// parent namespace with the tenant name, so deep nesting can exceed the
// limit even when each individual name passes the per-name Helm release
// length check.
//
// The "tenant-root" branches of computeTenantNamespace do not get a
// dedicated overflow case: that branch produces "tenant-<name>" (7 +
// len(name)), so for the computed result to exceed 63 chars the name
// would need to exceed 56 chars, which is already blocked by
// validateNameLength (max 46 for the "tenant-" prefix). The invariant
// holds by arithmetic.
func TestValidateTenantNamespaceLength(t *testing.T) {
	tests := []struct {
		name             string
		currentNamespace string
		tenantName       string
		wantError        bool
	}{
		{
			name:             "tenant-root with short name passes",
			currentNamespace: "tenant-root",
			tenantName:       "alpha",
			wantError:        false,
		},
		{
			name:             "root tenant inside root namespace passes",
			currentNamespace: "tenant-root",
			tenantName:       "root",
			wantError:        false,
		},
		{
			name:             "short parent and short name passes",
			currentNamespace: "tenant-foo",
			tenantName:       "bar",
			wantError:        false,
		},
		{
			name:             "exactly at 63-char limit passes",
			currentNamespace: "tenant-" + strings.Repeat("a", 45), // 52 chars
			tenantName:       strings.Repeat("b", 10),             // 10 chars -> 52+1+10 = 63
			wantError:        false,
		},
		{
			name:             "one char over the limit fails",
			currentNamespace: "tenant-" + strings.Repeat("a", 45), // 52 chars
			tenantName:       strings.Repeat("b", 11),             // 11 chars -> 52+1+11 = 64
			wantError:        true,
		},
		{
			name:             "deeply nested failure with issue-style names",
			currentNamespace: "tenant-" + strings.Repeat("a", 35), // 42 chars
			tenantName:       strings.Repeat("b", 25),             // 25 chars -> 42+1+25 = 68
			wantError:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &REST{
				kindName: "Tenant",
				releaseConfig: config.ReleaseConfig{
					Prefix: "tenant-",
				},
			}

			errs := r.validateTenantNamespaceLength(tt.currentNamespace, tt.tenantName)

			if tt.wantError && len(errs) == 0 {
				computed := r.computeTenantNamespace(tt.currentNamespace, tt.tenantName)
				t.Errorf("expected error for parent=%q name=%q (computed=%q, len=%d), got none",
					tt.currentNamespace, tt.tenantName, computed, len(computed))
				return
			}
			if !tt.wantError && len(errs) > 0 {
				t.Errorf("unexpected error for parent=%q name=%q: %v",
					tt.currentNamespace, tt.tenantName, errs)
				return
			}

			// For failing cases, verify the error message surfaces the
			// computed namespace string and its actual length so a
			// regression in the message format is caught.
			if tt.wantError {
				computed := r.computeTenantNamespace(tt.currentNamespace, tt.tenantName)
				msg := errs.ToAggregate().Error()
				if !strings.Contains(msg, computed) {
					t.Errorf("error message must contain computed namespace %q, got: %s", computed, msg)
				}
				if !strings.Contains(msg, fmt.Sprintf("%d characters", len(computed))) {
					t.Errorf("error message must contain the computed length %d, got: %s", len(computed), msg)
				}
			}
		})
	}
}
