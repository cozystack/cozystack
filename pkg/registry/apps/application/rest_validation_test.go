/*
Copyright 2025 The Cozystack Authors.

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
	"strings"
	"testing"

	"github.com/cozystack/cozystack/pkg/config"
)

func TestValidateNameLength(t *testing.T) {
	tests := []struct {
		name      string
		kindName  string
		prefix    string
		rootHost  string
		appName   string
		wantError bool
	}{
		{
			name:      "non-tenant short name passes",
			kindName:  "MySQL",
			prefix:    "mysql-",
			rootHost:  "example.com",
			appName:   "mydb",
			wantError: false,
		},
		{
			name:      "non-tenant at helm boundary passes",
			kindName:  "MySQL",
			prefix:    "mysql-",
			rootHost:  "example.com",
			appName:   strings.Repeat("a", 53-len("mysql-")), // exactly 47 chars
			wantError: false,
		},
		{
			name:      "non-tenant exceeding helm limit fails",
			kindName:  "MySQL",
			prefix:    "mysql-",
			rootHost:  "example.com",
			appName:   strings.Repeat("a", 53-len("mysql-")+1), // 48 chars
			wantError: true,
		},
		{
			name:      "tenant no rootHost within helm limit passes",
			kindName:  "Tenant",
			prefix:    "tenant-",
			rootHost:  "",
			appName:   strings.Repeat("a", 53-len("tenant-")), // 46 chars
			wantError: false,
		},
		{
			name:      "tenant no rootHost exceeding helm limit fails",
			kindName:  "Tenant",
			prefix:    "tenant-",
			rootHost:  "",
			appName:   strings.Repeat("a", 53-len("tenant-")+1), // 47 chars
			wantError: true,
		},
		{
			name:      "tenant with rootHost within both limits passes",
			kindName:  "Tenant",
			prefix:    "tenant-",
			rootHost:  "example.com", // 11 chars → host label max = 63-11-1 = 51
			appName:   "short",
			wantError: false,
		},
		{
			name:     "tenant with short rootHost helm limit is still stricter",
			kindName: "Tenant",
			prefix:   "tenant-",
			rootHost: "example.com",                          // 11 chars → host label max = 51, helm max = 46
			appName:  strings.Repeat("a", 53-len("tenant-")), // 46 chars — at Helm boundary
			wantError: false,
		},
		{
			name:     "tenant with long rootHost at host label boundary passes",
			kindName: "Tenant",
			prefix:   "tenant-",
			rootHost: "long-subdomain.hosting.example.com",                                    // 34 chars → host label max = 63-34-1 = 28
			appName:  strings.Repeat("a", 63-len("long-subdomain.hosting.example.com")-1), // exactly 28 chars
			wantError: false,
		},
		{
			name:     "tenant with long rootHost exceeding host label limit fails",
			kindName: "Tenant",
			prefix:   "tenant-",
			rootHost: "long-subdomain.hosting.example.com",                                      // 34 chars → host label max = 28
			appName:  strings.Repeat("a", 63-len("long-subdomain.hosting.example.com")-1+1), // 29 chars
			wantError: true,
		},
		{
			name:      "tenant host label limit stricter than helm limit",
			kindName:  "Tenant",
			prefix:    "tenant-",
			rootHost:  "very-long-subdomain.hosting.example.com", // 39 chars → host label max = 63-39-1 = 23
			appName:   strings.Repeat("a", 24),                   // 24 > 23 (host limit) but < 46 (helm limit)
			wantError: true,
		},
		{
			name:      "tenant short rootHost where helm limit is stricter",
			kindName:  "Tenant",
			prefix:    "tenant-",
			rootHost:  "x.co",                                     // 4 chars → host label max = 63-4-1 = 58, helm max = 46
			appName:   strings.Repeat("a", 53-len("tenant-")+1), // 47 > 46 (helm limit) but < 58 (host limit)
			wantError: true,
		},
		{
			name:      "non-tenant ignores rootHost for limit calculation",
			kindName:  "MySQL",
			prefix:    "mysql-",
			rootHost:  "very-long-subdomain.hosting.example.com",
			appName:   strings.Repeat("a", 53-len("mysql-")), // at helm boundary
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &REST{
				kindName: tt.kindName,
				releaseConfig: config.ReleaseConfig{
					Prefix: tt.prefix,
				},
				rootHost: tt.rootHost,
			}

			err := r.validateNameLength(tt.appName)

			if tt.wantError && err == nil {
				t.Errorf("expected error for name %q (len=%d), got nil", tt.appName, len(tt.appName))
			}
			if !tt.wantError && err != nil {
				t.Errorf("unexpected error for name %q (len=%d): %v", tt.appName, len(tt.appName), err)
			}
		})
	}
}
