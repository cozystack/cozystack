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
