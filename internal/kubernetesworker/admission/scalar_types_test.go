package admission

import (
	"context"
	"strings"
	"testing"

	"github.com/cozystack/cozystack/internal/kubernetesworker/config"
	av1 "k8s.io/api/admission/v1"
)

func TestMalformedPoolScalarsFailClosed(t *testing.T) {
	fields := []struct {
		path string
		kind string
	}{
		{"version", "string"},
		{"substrate", "string"},
		{"osImage.factory", "object"},
		{"osImage.builtin", "object"},
		{"osImage.factory.version", "string"},
		{"osImage.factory.schematicID", "string"},
		{"osImage.factory.imageFactoryURL", "string"},
		{"talos.installerRepository", "string"},
		{"talos.imageFactoryCA", "string"},
		{"talos.registryMirrors", "object"},
	}
	values := []struct {
		name  string
		value any
	}{
		{"zero", 0},
		{"number", 123},
		{"false", false},
		{"true", true},
		{"list", []any{}},
		{"map", map[string]any{}},
	}
	validator := &Validator{Reader: reader(t)}
	for _, field := range fields {
		for _, value := range values {
			if field.kind == "object" && value.name == "map" {
				continue
			}
			t.Run(field.path+"/"+value.name, func(t *testing.T) {
				pool := map[string]any{"workerClassName": "talos", "version": "v1.35"}
				current := pool
				path := strings.Split(field.path, ".")
				for _, key := range path[:len(path)-1] {
					next := map[string]any{}
					current[key] = next
					current = next
				}
				current[path[len(path)-1]] = value.value
				if _, err := config.Resolve(nil, pool); err == nil {
					t.Fatal("malformed consumed scalar silently resolved")
				}
				response := validator.Handle(context.Background(), req(av1.Update, pool, pool))
				if response.Allowed {
					t.Fatalf("malformed consumed scalar admitted: %v", response)
				}
			})
		}
	}
}

func TestPoolMinorDefaultsOnlyWhenUnspecified(t *testing.T) {
	validator := &Validator{Reader: reader(t)}
	for _, tc := range []struct {
		name  string
		pool  map[string]any
		allow bool
	}{
		{"omitted", map[string]any{"workerClassName": "talos"}, true},
		{"offered minor", map[string]any{"workerClassName": "talos", "version": "v1.35"}, true},
		{"explicit empty", map[string]any{"workerClassName": "talos", "version": ""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := validator.Handle(context.Background(), req(av1.Update, tc.pool, tc.pool))
			if response.Allowed != tc.allow {
				t.Fatalf("allowed=%v result=%v, want %v", response.Allowed, response.Result, tc.allow)
			}
		})
	}
}

func TestKairosRejectsMalformedEmptyContainers(t *testing.T) {
	validator := &Validator{Reader: reader(t)}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"talos", []any{}},
		{"osImage", []any{}},
		{"kernelModules", map[string]any{}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			pool := map[string]any{"workerClassName": "kairos", "version": "v1.35", tc.field: tc.value}
			response := validator.Handle(context.Background(), req(av1.Update, pool, pool))
			if response.Allowed {
				t.Fatalf("wrong container type became an empty supported value: %v", response)
			}
		})
	}
}
