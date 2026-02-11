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

package server

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestParseRootHostFromSecret(t *testing.T) {
	tests := []struct {
		name     string
		secret   *corev1.Secret
		expected string
	}{
		{
			name: "valid secret with root-host",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"values.yaml": []byte("_cluster:\n  root-host: \"example.com\"\n  bundle-name: \"paas-full\"\n"),
				},
			},
			expected: "example.com",
		},
		{
			name: "valid secret with unquoted root-host",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"values.yaml": []byte("_cluster:\n  root-host: my.domain.org\n"),
				},
			},
			expected: "my.domain.org",
		},
		{
			name: "missing values.yaml key",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"other-key": []byte("some data"),
				},
			},
			expected: "",
		},
		{
			name: "malformed YAML",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"values.yaml": []byte("not: valid: yaml: [[["),
				},
			},
			expected: "",
		},
		{
			name: "empty root-host",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"values.yaml": []byte("_cluster:\n  root-host: \"\"\n"),
				},
			},
			expected: "",
		},
		{
			name: "no _cluster key",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"values.yaml": []byte("other:\n  key: value\n"),
				},
			},
			expected: "",
		},
		{
			name: "_cluster without root-host",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"values.yaml": []byte("_cluster:\n  bundle-name: \"paas-full\"\n"),
				},
			},
			expected: "",
		},
		{
			name:     "nil data",
			secret:   &corev1.Secret{},
			expected: "",
		},
		{
			name:     "nil secret",
			secret:   nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseRootHostFromSecret(tt.secret)
			if result != tt.expected {
				t.Errorf("parseRootHostFromSecret() = %q, want %q", result, tt.expected)
			}
		})
	}
}
