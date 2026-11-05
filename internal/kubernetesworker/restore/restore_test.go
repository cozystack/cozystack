package restore

import (
	"encoding/json"
	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"strings"
	"testing"
)

func TestMaterializesTalosBaselineAndRemovesOnlyClassification(t *testing.T) {
	version := "v1.13.7"
	mirrors := map[string]workers.RegistryMirror{}
	class := &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos, Talos: &workers.TalosSettings{Version: &version, RegistryMirrors: &mirrors}, KernelModules: &[]workers.KernelModule{}}
	old := map[string]any{"cluster": "k", "workerClassName": "custom", "maxReplicas": 3, "talos": map[string]any{"imageFactoryURL": "https://factory.example.com", "installerRepository": "registry.example.com/installer"}, "labels": map[string]any{"example.com/team": "storage"}}
	got, err := Prepare(class, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["workerClassName"]; ok {
		t.Fatal("classification survived restoration")
	}
	if old["workerClassName"] != "custom" {
		t.Fatal("input mutated")
	}
	talos := got["talos"].(map[string]any)
	if talos["version"] != version || talos["installerRepository"] != "registry.example.com/installer" || got["maxReplicas"] != float64(3) {
		t.Fatalf("restored=%v", got)
	}
	raw, _ := json.Marshal(got["kernelModules"])
	if string(raw) != "[]" {
		t.Fatalf("module opt-out lost: %s", raw)
	}
}

func TestRefusesKairosAndAlreadyLegacy(t *testing.T) {
	for _, tc := range []struct {
		class  *workers.WorkerClassSpec
		values map[string]any
	}{
		{&workers.WorkerClassSpec{Bootstrap: workers.BootstrapKairos}, map[string]any{"workerClassName": "kairos"}},
		{nil, map[string]any{"cluster": "k"}},
	} {
		if _, err := Prepare(tc.class, tc.values); err == nil {
			t.Fatal("unsafe restoration accepted")
		}
	}
}

func TestRenderProofIsNotVacuousAndDetectsRollout(t *testing.T) {
	rendered := []byte("apiVersion: cluster.x-k8s.io/v1beta1\nkind: MachineDeployment\nmetadata:\n  name: pool\nspec:\n  template:\n    spec:\n      bootstrap:\n        configRef: {name: config-a}\n---\napiVersion: infrastructure.cluster.x-k8s.io/v1alpha1\nkind: KubevirtMachineTemplate\nmetadata:\n  name: vm-a\nspec:\n  template: {memory: 4Gi}\n")
	if err := VerifyRendered(rendered, rendered); err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.ReplaceAll(string(rendered), "config-a", "config-b"))
	if err := VerifyRendered(rendered, changed); err == nil {
		t.Fatal("changed bootstrap passed proof")
	}
	if err := VerifyRendered(nil, nil); err == nil {
		t.Fatal("empty render passed proof")
	}
}
