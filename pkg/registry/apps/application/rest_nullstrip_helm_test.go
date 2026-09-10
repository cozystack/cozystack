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
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"sigs.k8s.io/yaml"
)

// Schema defaults describe API reads; Helm coalesces values.yaml instead. Check
// their effective values independently so drift cannot turn repair into omission.
func TestRequiredNullDefaultsMatchChartValues(t *testing.T) {
	for _, tt := range nullChartCases {
		t.Run(tt.name, func(t *testing.T) {
			if tt.chart == "" {
				return
			}
			s := chartSpecSchema(t, tt.rd)
			raw, err := os.ReadFile("../../../../packages/" + tt.chart + "/values.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := yaml.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			var visit func(*structuralschema.Structural, map[string]any, string)
			visit = func(schema *structuralschema.Structural, defaults map[string]any, path string) {
				for key, prop := range schema.Properties {
					value, present := defaults[key]
					if _, required := requiredNames(schema)[key]; required && prop.Default.Object != nil && !prop.Nullable {
						if !present {
							t.Errorf("%s%s schema default has no chart value", path, key)
							continue
						}
						// Expand nested defaults using the existing API read path, without the
						// null normalizer, then compare semantically to the shipped chart value.
						raw, err := json.Marshal(map[string]any{key: prop.Default.Object})
						if err != nil {
							t.Fatal(err)
						}
						app := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: raw}}
						if err := (&REST{specSchema: schema}).applySpecDefaults(app); err != nil {
							t.Fatal(err)
						}
						expanded := jsonValue(t, app.Spec.Raw).(map[string]any)[key]
						rawValue, err := json.Marshal(value)
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(expanded, jsonValue(t, rawValue)) {
							t.Errorf("%s%s effective schema default differs from chart: %v != %v", path, key, expanded, value)
						}
					}
					if child, ok := value.(map[string]any); ok {
						visit(&prop, child, path+key+".")
					}
				}
			}
			visit(s, values, "")
		})
	}
}

// A minimal rendering template exposes Helm's real coalesced values while the
// values and validation schema remain the shipped files, without chart-specific
// cluster lookups or templates obscuring defaulting failures.
func TestRequiredNullHelmCoalescing(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm executable required for the coalescing integration test")
	}
	for _, tt := range nullChartCases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "templates"), 0755); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{"values.yaml", "values.schema.json"} {
				var data []byte
				if tt.chart != "" {
					var err error
					data, err = os.ReadFile("../../../../packages/" + tt.chart + "/" + file)
					if err != nil {
						t.Fatal(err)
					}
				} else if file == "values.yaml" {
					data = []byte(tt.values)
				} else {
					data = []byte(tt.schema)
				}
				if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for file, body := range map[string]string{"Chart.yaml": "apiVersion: v2\nname: null-contract\nversion: 0.0.0\n", "templates/values.yaml": "{{ .Values | toJson }}\n"} {
				if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			render := func(input []byte) ([]byte, error) {
				values := filepath.Join(dir, "input.json")
				if err := os.WriteFile(values, input, 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				return exec.CommandContext(ctx, helm, "template", "null-contract", dir, "-f", values).CombinedOutput()
			}
			app := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(tt.input)}}
			paths, err := (&REST{specSchema: nullCaseSchema(t, tt)}).normalizeSpecNulls(app)
			if err != nil {
				t.Fatal(err)
			}
			before, beforeErr := render([]byte(tt.input))
			after, afterErr := render(app.Spec.Raw)
			if len(tt.paths) == 0 {
				if len(paths) != 0 || string(app.Spec.Raw) != tt.input {
					t.Fatalf("nonrepairable input changed: %s", app.Spec.Raw)
				}
				if (beforeErr == nil) != (afterErr == nil) {
					t.Fatalf("normalization changed validation outcome: %s / %s", before, after)
				}
				if tt.name != "optional section" && beforeErr == nil {
					t.Fatalf("required null unexpectedly accepted: %s", before)
				}
				return
			}
			if beforeErr == nil {
				t.Fatalf("required-null input unexpectedly accepted by Helm: %s", before)
			}
			if afterErr != nil {
				t.Fatalf("normalized input failed Helm: %v\n%s", afterErr, after)
			}
			var rendered map[string]any
			if err := yaml.Unmarshal(after, &rendered); err != nil {
				t.Fatal(err)
			}
			chartValues, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var defaults map[string]any
			if err := yaml.Unmarshal(chartValues, &defaults); err != nil {
				t.Fatal(err)
			}
			at := func(v map[string]any, path string) any {
				var value any = v
				for _, key := range strings.Split(path, ".") {
					value = value.(map[string]any)[key]
				}
				return value
			}
			for _, path := range tt.paths {
				if !reflect.DeepEqual(at(rendered, path), at(defaults, path)) {
					t.Errorf("Helm failed to restore %s", path)
				}
			}
		})
	}
}
