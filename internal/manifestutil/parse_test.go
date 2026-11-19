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

package manifestutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadYAMLObjects(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantErr   bool
	}{
		{
			name: "single document",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`,
			wantCount: 1,
		},
		{
			name: "multiple documents",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test1
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: test2
`,
			wantCount: 2,
		},
		{
			name:      "empty input",
			input:     "",
			wantCount: 0,
		},
		{
			name: "decoder rejects document without kind",
			input: `apiVersion: v1
metadata:
  name: test
`,
			wantErr: true,
		},
		{
			name: "whitespace-only document between separators is skipped",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test1
---

---
apiVersion: v1
kind: ConfigMap
metadata:
  name: test2
`,
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects, err := ReadYAMLObjects(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadYAMLObjects() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(objects) != tt.wantCount {
				t.Errorf("ReadYAMLObjects() returned %d objects, want %d", len(objects), tt.wantCount)
			}
		})
	}
}

func TestReadYAMLObjects_preservesFields(t *testing.T) {
	input := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: packages.cozystack.io
spec:
  group: cozystack.io
`
	objects, err := ReadYAMLObjects(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ReadYAMLObjects() error = %v", err)
	}
	if len(objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objects))
	}

	obj := objects[0]
	if obj.GetKind() != "CustomResourceDefinition" {
		t.Errorf("kind = %q, want %q", obj.GetKind(), "CustomResourceDefinition")
	}
	if obj.GetName() != "packages.cozystack.io" {
		t.Errorf("name = %q, want %q", obj.GetName(), "packages.cozystack.io")
	}
	if obj.GetAPIVersion() != "apiextensions.k8s.io/v1" {
		t.Errorf("apiVersion = %q, want %q", obj.GetAPIVersion(), "apiextensions.k8s.io/v1")
	}
}

func TestParseManifestFile(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "test.yaml")

	content := `apiVersion: v1
kind: ConfigMap
metadata:
  name: cm1
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm2
`
	if err := os.WriteFile(manifestPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test manifest: %v", err)
	}

	objects, err := ParseManifestFile(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifestFile() error = %v", err)
	}
	if len(objects) != 2 {
		t.Errorf("ParseManifestFile() returned %d objects, want 2", len(objects))
	}
}

func TestParseManifestFile_notFound(t *testing.T) {
	_, err := ParseManifestFile("/nonexistent/path/test.yaml")
	if err == nil {
		t.Error("ParseManifestFile() expected error for nonexistent file, got nil")
	}
}
