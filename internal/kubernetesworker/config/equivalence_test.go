package config

import (
	"encoding/json"
	"maps"
	"reflect"
	"testing"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
)

func TestEquivalentIgnoresSourceOverriddenTalosFallbacks(t *testing.T) {
	for _, tc := range []struct {
		source, field, selected, old string
	}{
		{"factory", "version", "v1.13.6", "v1.13.5"},
		{"factory", "schematicID", "custom123", "old123"},
		{"factory", "imageFactoryURL", "https://factory.example.com", "https://old.example.com"},
		{"builtin", "version", "v1.13.6", "v1.13.5"},
		{"builtin", "schematicID", "custom123", "old123"},
	} {
		t.Run(tc.source+"/"+tc.field, func(t *testing.T) {
			pool := func(fallback string) map[string]any {
				return map[string]any{
					"talos":   map[string]any{tc.field: fallback},
					"osImage": map[string]any{tc.source: map[string]any{tc.field: tc.selected}},
				}
			}
			old, err := Resolve(nil, pool(tc.old))
			if err != nil {
				t.Fatal(err)
			}
			current, err := Resolve(&workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}, pool(tc.selected))
			if err != nil {
				t.Fatal(err)
			}
			beforeOld, _ := json.Marshal(old)
			beforeCurrent, _ := json.Marshal(current)
			if !Equivalent(old, current) {
				t.Errorf("selected %s overrides unused talos.%s", tc.source, tc.field)
			}
			afterOld, _ := json.Marshal(old)
			afterCurrent, _ := json.Marshal(current)
			if string(beforeOld) != string(afterOld) || string(beforeCurrent) != string(afterCurrent) {
				t.Fatal("equivalence comparison mutated resolved inputs")
			}
		})
	}
}

func TestEquivalentIgnoresInactiveImportSettings(t *testing.T) {
	for _, source := range []string{"builtin", "proxmox"} {
		for _, field := range []string{"imageFactoryURL", "imageFactoryCA"} {
			t.Run(source+"/"+field, func(t *testing.T) {
				pool := func(value string) map[string]any {
					values := map[string]any{"talos": map[string]any{field: value}}
					if source == "builtin" {
						values["osImage"] = map[string]any{"builtin": map[string]any{}}
					} else {
						values["substrate"] = "proxmox"
						values["proxmox"] = map[string]any{"templateTags": []any{"talos-worker"}}
					}
					return values
				}
				old, err := Resolve(nil, pool("https://old.example.com"))
				if err != nil {
					t.Fatal(err)
				}
				current, err := Resolve(&workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}, pool("https://new.example.com"))
				if err != nil {
					t.Fatal(err)
				}
				beforeOld, _ := json.Marshal(old)
				beforeCurrent, _ := json.Marshal(current)
				if !Equivalent(old, current) {
					t.Errorf("%s does not consume %s", source, field)
				}
				afterOld, _ := json.Marshal(old)
				afterCurrent, _ := json.Marshal(current)
				if string(beforeOld) != string(afterOld) || string(beforeCurrent) != string(afterCurrent) {
					t.Fatal("equivalence comparison mutated resolved inputs")
				}
			})
		}
	}
}

func TestEquivalentRetainsConsumedTalosSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		pool map[string]any
	}{
		{"image version", map[string]any{"osImage": map[string]any{"factory": map[string]any{"version": "v1.13.7"}}}},
		{"image schematic", map[string]any{"osImage": map[string]any{"factory": map[string]any{"version": "v1.13.6", "schematicID": "custom123"}}}},
		{"HTTP factory URL", map[string]any{"osImage": map[string]any{"factory": map[string]any{"version": "v1.13.6", "imageFactoryURL": "https://factory.example.com"}}}},
		{"image source", map[string]any{"osImage": map[string]any{"builtin": map[string]any{"version": "v1.13.6"}}}},
		{"installer", map[string]any{"talos": map[string]any{"installerRepository": "registry.example.com/installer"}}},
		{"mirrors", map[string]any{"talos": map[string]any{"registryMirrors": map[string]any{"docker.io": map[string]any{"endpoints": []any{"https://mirror.example.com"}}}}}},
		{"HTTP import CA", map[string]any{"talos": map[string]any{"imageFactoryCA": "custom-ca"}}},
		{"kernel modules", map[string]any{"kernelModules": []any{map[string]any{"name": "nvme_tcp"}}}},
		{"Kubernetes version", map[string]any{"version": "v1.34"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := map[string]any{"osImage": map[string]any{"factory": map[string]any{"version": "v1.13.6"}}}
			old, err := Resolve(nil, pool)
			if err != nil {
				t.Fatal(err)
			}
			maps.Copy(pool, tc.pool)
			current, err := Resolve(&workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}, pool)
			if err != nil {
				t.Fatal(err)
			}
			if Equivalent(old, current) {
				t.Fatal("changed consumer configuration accepted as equivalent")
			}
		})
	}
}

func TestEquivalentPreservesProxmoxTalosIdentity(t *testing.T) {
	pool := map[string]any{"substrate": "proxmox", "proxmox": map[string]any{"templateTags": []any{"talos-worker"}}}
	old, err := Resolve(nil, pool)
	if err != nil {
		t.Fatal(err)
	}
	pool["talos"] = map[string]any{"version": "v1.13.7"}
	current, err := Resolve(&workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}, pool)
	if err != nil {
		t.Fatal(err)
	}
	if Equivalent(old, current) {
		t.Fatal("changed Proxmox template compatibility accepted")
	}
}

func TestEquivalentDoesNotRelaxFixedImageContract(t *testing.T) {
	class := &workers.WorkerClassSpec{
		Bootstrap: workers.BootstrapTalos,
		Substrates: &workers.Substrates{KubeVirt: &workers.KubeVirtSource{
			OSImage: &workers.TalosImageSource{Builtin: &workers.BuiltinSource{Version: "v1.13.6", SchematicID: "custom123"}},
		}},
	}
	pool := map[string]any{"osImage": map[string]any{"factory": map[string]any{"version": "v1.13.6", "schematicID": "custom123"}}}
	before, _ := json.Marshal(pool)
	if _, err := Resolve(class, pool); err == nil {
		t.Fatal("fixed builtin artifact replaced with factory source")
	}
	after, _ := json.Marshal(pool)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("fixed-image refusal mutated input")
	}
}

// The fixed Proxmox contract compares template tags as a set, so the order a
// class or a pool lists them in must not decide adoption either.
func TestEquivalentComparesProxmoxTemplateTagsAsASet(t *testing.T) {
	pool := map[string]any{"substrate": "proxmox", "proxmox": map[string]any{"templateTags": []any{"talos-worker", "zone-a"}}}
	old, err := Resolve(nil, pool)
	if err != nil {
		t.Fatal(err)
	}
	pool["proxmox"] = map[string]any{"templateTags": []any{"zone-a", "talos-worker"}}
	current, err := Resolve(&workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !Equivalent(old, current) {
		t.Fatal("reordered Proxmox template tags refused as a different template")
	}
}
