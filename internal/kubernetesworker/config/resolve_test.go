package config

import (
	"encoding/json"
	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"os"
	"reflect"
	"sigs.k8s.io/yaml"
	"strings"
	"testing"
)

// The policy defaults move with releases while legacyPools stay frozen, so a
// pool without a class must keep a stored value even when it equals one.
func TestStoredDefaultsYieldOnlyToAClass(t *testing.T) {
	defaults := policyDefaults{LegacyPools: []map[string]any{
		{"talos": map[string]any{"version": "v1.13.6"}},
		{"talos": map[string]any{"version": "v1.13.12"}},
	}}
	for _, version := range []string{"v1.13.6", "v1.13.12"} {
		if defaults.storedDefault(nil, version, "talos", "version") {
			t.Fatalf("a pool without a class lost its stored talos.version %s", version)
		}
		if !defaults.storedDefault(&workers.WorkerClassSpec{}, version, "talos", "version") {
			t.Fatalf("a classed pool kept the stored talos.version %s over its class", version)
		}
	}
	if defaults.storedDefault(&workers.WorkerClassSpec{}, "v1.13.7", "talos", "version") {
		t.Fatal("an explicit talos.version was taken for the stored default")
	}
}

// Every Talos release the old schema defaulted to is stored on some pool, so
// a classed pool on either follows its class.
func TestEachLegacyTalosVersionYieldsToTheClass(t *testing.T) {
	classVersion := "v1.13.9"
	class := &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos, Talos: &workers.TalosSettings{Version: &classVersion}}
	for _, version := range []string{"v1.13.6", "v1.13.12"} {
		got, err := Resolve(class, map[string]any{ClassField: "talos", "version": "v1.35", "talos": map[string]any{"version": version}})
		if err != nil {
			t.Fatal(err)
		}
		if got.TalosVersion != "v1.13.9" {
			t.Fatalf("stored talos.version %s resolved to %s, want the class's v1.13.9", version, got.TalosVersion)
		}
	}
}

func TestTalosSourceVersionOverridesUnusedFallback(t *testing.T) {
	for _, source := range []string{"factory", "builtin"} {
		t.Run(source, func(t *testing.T) {
			got, err := Resolve(&workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}, map[string]any{
				ClassField: "talos",
				"version":  "v1.35",
				"talos":    map[string]any{"version": ""},
				"osImage":  map[string]any{source: map[string]any{"version": "v1.13.6"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.TalosVersion != "v1.13.6" || got.Source != source {
				t.Fatalf("resolution=%+v, want version v1.13.6 and source %s", got, source)
			}
		})
	}
}

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
		ImageFactoryCA   *string                  `json:"imageFactoryCA"`
		ImageFactoryURL  *string                  `json:"imageFactoryURL"`
		Installer        *string                  `json:"installerRepository"`
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
			if tc.ImageFactoryURL != nil && got.Talos["imageFactoryURL"] != *tc.ImageFactoryURL {
				t.Fatalf("imageFactoryURL=%v want %q", got.Talos["imageFactoryURL"], *tc.ImageFactoryURL)
			}
			if tc.Installer != nil && got.Talos["installerRepository"] != *tc.Installer {
				t.Fatalf("installerRepository=%v want %q", got.Talos["installerRepository"], *tc.Installer)
			}
			if tc.ImageFactoryCA != nil && got.Talos["imageFactoryCA"] != *tc.ImageFactoryCA {
				t.Fatalf("imageFactoryCA=%v want %q", got.Talos["imageFactoryCA"], *tc.ImageFactoryCA)
			}
			if tc.Mirrors != nil && !reflect.DeepEqual(got.Talos["registryMirrors"], tc.Mirrors) {
				t.Fatalf("mirrors=%v want %v", got.Talos["registryMirrors"], tc.Mirrors)
			}
		})
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

func TestResolveRefusesWhatTheChartCannotRender(t *testing.T) {
	kairos := &workers.WorkerClassSpec{Bootstrap: workers.BootstrapKairos}
	talos := &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}
	oldTalos := "v1.13.6"
	unsupported := &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos, Talos: &workers.TalosSettings{Version: &oldTalos}}
	for _, tc := range []struct {
		name  string
		class *workers.WorkerClassSpec
		pool  map[string]any
		want  string
	}{
		{"Kairos on Proxmox", kairos, map[string]any{"workerClassName": "custom", "substrate": "proxmox"}, "bootstrap kairos requires substrate kubevirt"},
		{"a Talos release that does not support the pool's Kubernetes", unsupported, map[string]any{"workerClassName": "custom", "version": "v1.30"}, "talos.version v1.13.6 does not support Kubernetes v1.30"},
		{"a version without the v prefix", talos, map[string]any{"workerClassName": "custom", "version": "1.35"}, `version "1.35" must be a Kubernetes minor`},
		{"a patch version", talos, map[string]any{"workerClassName": "custom", "version": "v1.35.0"}, `version "v1.35.0" must be a Kubernetes minor`},
		{"an unknown bootstrap", &workers.WorkerClassSpec{Bootstrap: "flatcar"}, map[string]any{"workerClassName": "custom"}, `unsupported bootstrap "flatcar"`},
		{"an unknown substrate", talos, map[string]any{"workerClassName": "custom", "substrate": "vsphere"}, `unsupported substrate "vsphere"`},
		{"a null kernelModules", talos, map[string]any{"workerClassName": "custom", "kernelModules": nil}, "kernelModules: got null, want array"},
		{"an osImage on Proxmox", talos, map[string]any{"workerClassName": "custom", "substrate": "proxmox", "osImage": map[string]any{"factory": map[string]any{"version": "v1.12.6"}}, "proxmox": map[string]any{"templateTags": []any{"template"}}}, "osImage is not a Proxmox source"},
		{"Proxmox without template tags", talos, map[string]any{"workerClassName": "custom", "substrate": "proxmox", "proxmox": map[string]any{"templateTags": []any{}}}, "proxmox.templateTags must not be empty"},
		{"a null osImage.builtin", talos, map[string]any{"workerClassName": "custom", "osImage": map[string]any{"builtin": nil}}, "osImage.builtin: got null, want object"},
		{"a null osImage.factory", talos, map[string]any{"workerClassName": "custom", "osImage": map[string]any{"factory": nil}}, "osImage.factory: got null, want object"},
		{"a null osImage.factory.version", talos, map[string]any{"workerClassName": "custom", "osImage": map[string]any{"factory": map[string]any{"version": nil}}}, "osImage.factory.version: got null, want string"},
		{"a null osImage.factory.schematicID", talos, map[string]any{"workerClassName": "custom", "osImage": map[string]any{"factory": map[string]any{"schematicID": nil}}}, "osImage.factory.schematicID: got null, want string"},
		{"a null osImage.factory.imageFactoryURL", talos, map[string]any{"workerClassName": "custom", "osImage": map[string]any{"factory": map[string]any{"imageFactoryURL": nil}}}, "osImage.factory.imageFactoryURL: got null, want string"},
		{"a null osImage.builtin.version", talos, map[string]any{"workerClassName": "custom", "osImage": map[string]any{"builtin": map[string]any{"version": nil}}}, "osImage.builtin.version: got null, want string"},
		{"a null osImage.builtin.schematicID", talos, map[string]any{"workerClassName": "custom", "osImage": map[string]any{"builtin": map[string]any{"schematicID": nil}}}, "osImage.builtin.schematicID: got null, want string"},
		{"a null talos.version", talos, map[string]any{"workerClassName": "custom", "talos": map[string]any{"version": nil}}, "talos.version: got null, want string"},
		{"a null talos.schematicID", talos, map[string]any{"workerClassName": "custom", "talos": map[string]any{"schematicID": nil}}, "talos.schematicID: got null, want string"},
		{"a null talos.imageFactoryURL", talos, map[string]any{"workerClassName": "custom", "talos": map[string]any{"imageFactoryURL": nil}}, "talos.imageFactoryURL: got null, want string"},
		{"a null talos.installerRepository", talos, map[string]any{"workerClassName": "custom", "talos": map[string]any{"installerRepository": nil}}, "talos.installerRepository: got null, want string"},
		{"Proxmox template tags that are not a list", talos, map[string]any{"workerClassName": "custom", "substrate": "proxmox", "proxmox": map[string]any{"templateTags": "template"}}, "proxmox.templateTags: must be a string list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(tc.class, tc.pool)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Every path the typed Go client writes as null (listed by that module's own
// test) resolves exactly as if it were absent, with and without a class.
func TestTypedClientNullsResolveAsUnset(t *testing.T) {
	raw, err := os.ReadFile("../../../api/apps/v1alpha1/kubernetesnodes/testdata/null-paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	if err = json.Unmarshal(raw, &paths); err != nil {
		t.Fatal(err)
	}
	talosClass := &workers.WorkerClassSpec{}
	if err = json.Unmarshal([]byte(`{"bootstrap":"talos","talos":{"registryMirrors":{"ghcr.io":{"endpoints":["https://baseline.example.com"]}}}}`), talosClass); err != nil {
		t.Fatal(err)
	}
	for _, class := range []*workers.WorkerClassSpec{nil, talosClass, {Bootstrap: workers.BootstrapKairos}} {
		for _, p := range paths {
			pool := map[string]any{}
			if class != nil {
				pool["workerClassName"] = "custom"
			}
			want, err := Resolve(class, pool)
			if err != nil {
				t.Fatalf("%s without the null: %v", p, err)
			}
			node := pool
			parts := strings.Split(p, ".")
			for _, part := range parts[:len(parts)-1] {
				next := map[string]any{}
				node[part] = next
				node = next
			}
			node[parts[len(parts)-1]] = nil
			got, err := Resolve(class, pool)
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s resolved %+v, want %+v", p, got, want)
			}
		}
	}
}

// A pool stored before WorkerClass may carry imageFactoryCA: null; it takes
// the empty default, as it did then.
func TestStoredNullImageFactoryCAIsTheEmptyDefault(t *testing.T) {
	got, err := Resolve(nil, map[string]any{"talos": map[string]any{"imageFactoryCA": nil}})
	if err != nil {
		t.Fatal(err)
	}
	if ca := got.Talos["imageFactoryCA"]; ca != "" {
		t.Fatalf("imageFactoryCA %#v, want the empty string", ca)
	}
}

// A pool stored with osImage: null before WorkerClass rendered, since Helm
// dropped the null against the chart default; it still resolves as if unset.
func TestANullOsImageResolvesAsUnset(t *testing.T) {
	fixed := &workers.WorkerClassSpec{}
	if err := json.Unmarshal([]byte(`{"bootstrap":"talos","substrates":{"kubevirt":{"osImage":{"builtin":{"version":"v1.13.6","schematicID":"fixed123"}}}}}`), fixed); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		class *workers.WorkerClassSpec
		pool  map[string]any
	}{
		{"legacy", nil, map[string]any{}},
		{"fixed builtin class", fixed, map[string]any{"workerClassName": "custom"}},
		{"kairos", &workers.WorkerClassSpec{Bootstrap: workers.BootstrapKairos}, map[string]any{"workerClassName": "custom"}},
		{"proxmox", &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}, map[string]any{"workerClassName": "custom", "substrate": "proxmox", "proxmox": map[string]any{"templateTags": []any{"talos-worker"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := Resolve(tc.class, tc.pool)
			if err != nil {
				t.Fatal(err)
			}
			tc.pool["osImage"] = nil
			got, err := Resolve(tc.class, tc.pool)
			if err != nil {
				t.Fatalf("with osImage: null: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("with osImage: null resolved %+v, want %+v", got, want)
			}
		})
	}
}

// proxmox.templateTags: null is the same case as osImage: null: optional with
// an empty chart default before WorkerClass, so a stored null rendered.
func TestANullTemplateTagsResolveAsUnset(t *testing.T) {
	fixed := &workers.WorkerClassSpec{}
	if err := json.Unmarshal([]byte(`{"bootstrap":"talos","talos":{"version":"v1.13.6","schematicID":"fixed123"},"substrates":{"proxmox":{"templateTags":["talos-worker"]}}}`), fixed); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		class *workers.WorkerClassSpec
		pool  map[string]any
	}{
		{"legacy kubevirt", nil, map[string]any{}},
		{"talos class kubevirt", &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}, map[string]any{"workerClassName": "custom"}},
		{"kairos", &workers.WorkerClassSpec{Bootstrap: workers.BootstrapKairos}, map[string]any{"workerClassName": "custom"}},
		{"proxmox class with fixed tags", fixed, map[string]any{"workerClassName": "custom", "substrate": "proxmox", "version": "v1.35"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := Resolve(tc.class, tc.pool)
			if err != nil {
				t.Fatal(err)
			}
			tc.pool["proxmox"] = map[string]any{"templateTags": nil}
			got, err := Resolve(tc.class, tc.pool)
			if err != nil {
				t.Fatalf("with templateTags: null: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("with templateTags: null resolved %+v, want %+v", got, want)
			}
		})
	}
}

func TestResourceDescriptorTracksGeneratedApplication(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/system/kubernetes-nodes-rd/cozyrds/kubernetes-nodes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var definition struct {
		Spec struct{ Application struct{ Kind, Plural string } }
	}
	if err = yaml.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	descriptor := PoolResource()
	if descriptor.Kind != definition.Spec.Application.Kind || descriptor.Resource != definition.Spec.Application.Plural {
		t.Fatalf("stale resource descriptor: %+v vs %v", descriptor, definition.Spec.Application)
	}
}

// The worker-classes chart ships its own copy of the descriptor; a copy that
// differs from the one embedded here would make the two match different
// resources.
func TestChartPoolResourceEqualsTheEmbeddedDescriptor(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/system/kubernetes-worker-classes/files/pool-resource.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(resourceJSON) {
		t.Fatalf("packages/system/kubernetes-worker-classes/files/pool-resource.json differs from resource.json; run make -C packages/apps/kubernetes-nodes generate")
	}
}
