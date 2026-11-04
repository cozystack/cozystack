package v1alpha1

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/runtime"
	"testing"
)

func TestPresenceSurvivesJSON(t *testing.T) {
	for _, raw := range []string{
		`{"bootstrap":"talos"}`,
		`{"bootstrap":"talos","kernelModules":[]}`,
		`{"bootstrap":"talos","talos":{"version":"","registryMirrors":{}}}`,
		`{"bootstrap":"talos","talos":{"registryMirrors":{"ghcr.io":{"endpoints":[],"skipFallback":false,"overridePath":false}}}}`,
	} {
		var spec WorkerClassSpec
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		var got, want map[string]any
		if err = json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("round-trip %s = %s", raw, gotJSON)
		}
	}
}

func TestIndependentScheme(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	obj, err := scheme.New(GroupVersion.WithKind("WorkerClass"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := obj.(*WorkerClass); !ok {
		t.Fatalf("registered type %T", obj)
	}
}
