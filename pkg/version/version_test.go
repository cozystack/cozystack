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

package version

import (
	"os"
	"testing"
)

// unsetenv clears key for the duration of the test and restores its previous
// value (or absence) afterwards, so the env-unset case exercises the fallback
// path even when COZYSTACK_VERSION leaks in from the test runner's environment.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestResolve(t *testing.T) {
	const baked = "v1.4.0-rc.2"

	tests := []struct {
		name string
		env  string
		set  bool
		want string
	}{
		{name: "env unset falls back to baked", set: false, want: baked},
		{name: "empty env falls back to baked", env: "", set: true, want: baked},
		{name: "env overrides baked", env: "v1.4.0", set: true, want: "v1.4.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("COZYSTACK_VERSION", tt.env)
			} else {
				unsetenv(t, "COZYSTACK_VERSION")
			}
			if got := resolve(baked); got != tt.want {
				t.Errorf("resolve(%q) with env set=%v value=%q = %q, want %q",
					baked, tt.set, tt.env, got, tt.want)
			}
		})
	}
}
