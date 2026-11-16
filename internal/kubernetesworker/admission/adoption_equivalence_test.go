package admission

import (
	"context"
	"testing"

	av1 "k8s.io/api/admission/v1"
)

func TestLegacyAdoptionIgnoresInactiveImportSettings(t *testing.T) {
	for _, source := range []string{"builtin", "proxmox"} {
		t.Run(source, func(t *testing.T) {
			pool := func(value string, classified bool) map[string]any {
				values := map[string]any{"talos": map[string]any{"imageFactoryURL": value, "imageFactoryCA": value}}
				if source == "builtin" {
					values["osImage"] = map[string]any{"builtin": map[string]any{}}
				} else {
					values["substrate"] = "proxmox"
					values["proxmox"] = map[string]any{"templateTags": []any{"talos-worker"}}
				}
				if classified {
					values["workerClassName"] = "talos"
				}
				return values
			}
			response := (&Validator{Reader: reader(t)}).Handle(context.Background(), req(av1.Update,
				pool("https://new.example.com", true), pool("https://old.example.com", false)))
			if !response.Allowed {
				t.Fatalf("unused import setting rejected: %v", response.Result)
			}
		})
	}
}

func TestLegacyAdoptionUsesSelectedFactoryVersion(t *testing.T) {
	pool := func(fallback, selected string, classified bool) map[string]any {
		values := map[string]any{
			"talos":   map[string]any{"version": fallback},
			"osImage": map[string]any{"factory": map[string]any{"version": selected}},
		}
		if classified {
			values["workerClassName"] = "talos"
		}
		return values
	}
	validator := &Validator{Reader: reader(t)}
	for _, tc := range []struct {
		name, selected string
		allowed        bool
	}{
		{"unchanged image", "v1.13.6", true},
		{"changed image", "v1.13.7", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := validator.Handle(context.Background(), req(av1.Update,
				pool("v1.13.6", tc.selected, true), pool("v1.13.5", "v1.13.6", false)))
			if response.Allowed != tc.allowed {
				t.Fatalf("allowed=%v result=%v, want %v", response.Allowed, response.Result, tc.allowed)
			}
		})
	}
}
