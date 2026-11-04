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
		Name        string                   `json:"name"`
		Class       *workers.WorkerClassSpec `json:"class"`
		Pool        map[string]any           `json:"pool"`
		Version     string                   `json:"version"`
		SchematicID string                   `json:"schematicID"`
		Source      string                   `json:"source"`
		Modules     []string                 `json:"modules"`
		Mirrors     map[string]any           `json:"mirrors"`
		Error       string                   `json:"error"`
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
			if tc.Mirrors != nil && !reflect.DeepEqual(got.Talos["registryMirrors"], tc.Mirrors) {
				t.Fatalf("mirrors=%v want %v", got.Talos["registryMirrors"], tc.Mirrors)
			}
		})
	}
}
