package config

import (
	"encoding/json"
	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestResolutionCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/resolution.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name             string                   `json:"name"`
		Class            *workers.WorkerClassSpec `json:"class"`
		Pool             map[string]any           `json:"pool"`
		Version          string                   `json:"version"`
		SchematicID      string                   `json:"schematicID"`
		Source           string                   `json:"source"`
		Modules          []string                 `json:"modules"`
		Mirrors          map[string]any           `json:"mirrors"`
		ModuleParameters map[string][]string      `json:"moduleParameters"`
		Error            string                   `json:"error"`
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := Resolve(tc.Class, tc.Pool)
			if tc.Error != "" {
				if err == nil || !strings.Contains(err.Error(), tc.Error) {
					t.Fatalf("error=%v, want %q", err, tc.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.TalosVersion != tc.Version || got.SchematicID != tc.SchematicID || got.Source != tc.Source {
				t.Fatalf("resolution=%+v, want version=%q schematic=%q source=%q", got, tc.Version, tc.SchematicID, tc.Source)
			}
			names := []string{}
			for _, m := range got.KernelModules {
				names = append(names, m.Name)
			}
			if !reflect.DeepEqual(names, tc.Modules) {
				t.Fatalf("modules=%v want %v", names, tc.Modules)
			}
			if tc.ModuleParameters != nil {
				gotParameters := map[string][]string{}
				for _, m := range got.KernelModules {
					if len(m.Parameters) > 0 {
						gotParameters[m.Name] = m.Parameters
					}
				}
				if !reflect.DeepEqual(gotParameters, tc.ModuleParameters) {
					t.Fatalf("module parameters=%v want %v", gotParameters, tc.ModuleParameters)
				}
			}
			if tc.Mirrors != nil && !reflect.DeepEqual(got.Talos["registryMirrors"], tc.Mirrors) {
				t.Fatalf("mirrors=%v want %v", got.Talos["registryMirrors"], tc.Mirrors)
			}
		})
	}
}

func TestPoolSchemaPreservesClassAndFamilyPresence(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/apps/kubernetes-nodes/values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	for _, field := range []string{"workerClassName", "talos", "osImage"} {
		property, exists := properties[field].(map[string]any)
		if !exists {
			t.Fatalf("schema has no %s", field)
		}
		if _, hasDefault := property["default"]; hasDefault {
			t.Errorf("%s has a schema default that destroys presence", field)
		}
	}
	talos := properties["talos"].(map[string]any)["properties"].(map[string]any)
	for field, raw := range talos {
		if _, hasDefault := raw.(map[string]any)["default"]; hasDefault {
			t.Errorf("talos.%s has a manufactured default", field)
		}
	}
}

func TestEquivalentFactorySelections(t *testing.T) {
	a, err := Resolve(nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	class := &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos, Substrates: &workers.Substrates{KubeVirt: &workers.KubeVirtSource{OSImage: &workers.TalosImageSource{Factory: &workers.FactorySource{}}}}}
	b, err := Resolve(class, map[string]any{"workerClassName": "custom"})
	if err != nil {
		t.Fatal(err)
	}
	if !Equivalent(a, b) {
		t.Fatal("equivalent factory syntax changes effective settings")
	}
}
