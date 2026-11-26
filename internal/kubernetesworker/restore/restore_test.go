package restore

import (
	"encoding/json"
	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"reflect"
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

func TestRenderProofComparesMachineDeploymentAnnotationsButTheClass(t *testing.T) {
	md := func(annotations string) []byte {
		return []byte("apiVersion: cluster.x-k8s.io/v1beta1\nkind: MachineDeployment\nmetadata:\n  name: pool\n  annotations:\n" + annotations + "spec: {replicas: 1}\n---\napiVersion: infrastructure.cluster.x-k8s.io/v1alpha1\nkind: KubevirtMachineTemplate\nmetadata:\n  name: vm-a\nspec: {}\n")
	}
	current := md("    cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: \"3\"\n    workerclasses.cozystack.io/class: talos\n")
	if err := VerifyRendered(current, md("    cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: \"3\"\n")); err != nil {
		t.Fatalf("the class annotation alone failed the proof: %v", err)
	}
	if err := VerifyRendered(md("    workerclasses.cozystack.io/class: talos\n"), []byte(strings.Replace(string(md("")), "  annotations:\n", "", 1))); err != nil {
		t.Fatalf("a legacy render without annotations failed the proof: %v", err)
	}
	if err := VerifyRendered(current, md("    cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: \"5\"\n")); err == nil {
		t.Fatal("a changed autoscaler bound passed the proof")
	}
}

func TestKeepsAutomaticModulesWhenNeitherClassNorPoolListsThem(t *testing.T) {
	class := &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos}
	for _, pool := range []map[string]any{
		{"cluster": "k", "workerClassName": "custom"},
		{"cluster": "k", "workerClassName": "custom", "gpus": []any{map[string]any{"name": "nvidia.com/GH200"}}},
	} {
		got, err := Prepare(class, pool)
		if err != nil {
			t.Fatal(err)
		}
		if modules, ok := got["kernelModules"]; ok {
			t.Fatalf("automatic module selection frozen as %v", modules)
		}
	}
	got, err := Prepare(class, map[string]any{"cluster": "k", "workerClassName": "custom", "kernelModules": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(got["kernelModules"]); string(raw) != "[]" {
		t.Fatalf("pool opt-out lost: %s", raw)
	}
}

// Of proxmox only the class's template tags become inline: the rest may come
// from valuesFrom and must stay inherited after the rollback.
func TestLegacyValuesFreezeOnlyClassInputs(t *testing.T) {
	inline := map[string]any{"workerClassName": "custom", "substrate": "proxmox", "proxmox": map[string]any{"dnsServers": []any{"192.0.2.53"}}}
	prepared := map[string]any{
		"talos":   map[string]any{"version": "v1.13.6"},
		"osImage": map[string]any{},
		"proxmox": map[string]any{"network": map[string]any{"bridge": "vmbr0"}, "dnsServers": []any{"192.0.2.53"}, "templateTags": []string{"template"}},
	}
	got := Legacy(inline, prepared)
	want := map[string]any{
		"substrate": "proxmox",
		"talos":     map[string]any{"version": "v1.13.6"},
		"osImage":   map[string]any{},
		"proxmox":   map[string]any{"dnsServers": []any{"192.0.2.53"}, "templateTags": []string{"template"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy values = %v, want %v", got, want)
	}
	if _, tagged := inline["proxmox"].(map[string]any)["templateTags"]; tagged || inline["workerClassName"] != "custom" {
		t.Fatalf("inline values were modified: %v", inline)
	}
	if got := Legacy(map[string]any{"workerClassName": "custom"}, prepared); !reflect.DeepEqual(got["proxmox"], map[string]any{"templateTags": []string{"template"}}) {
		t.Fatalf("proxmox without inline values = %v", got["proxmox"])
	}
}

// The pre-class chart renders an import CA ConfigMap for a Proxmox pool that
// sets one, which the class-era chart no longer renders; a Proxmox source
// imports nothing, so the CA must not be restored or the proof always fails.
func TestProxmoxRestorationLeavesOutTheImportCA(t *testing.T) {
	ca := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	version, schematic := "v1.13.6", "ce4c980550dd2ab1b17bbf2b08801c7eb59418eafe8f279833297925d67c7515"
	class := &workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos, Talos: &workers.TalosSettings{Version: &version, SchematicID: &schematic, ImageFactoryCA: &ca}}
	pool := map[string]any{"workerClassName": "custom", "substrate": "proxmox", "proxmox": map[string]any{"templateTags": []any{"talos-worker"}}}
	got, err := Prepare(class, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["talos"].(map[string]any)["imageFactoryCA"]; ok {
		t.Fatalf("a Proxmox restoration kept the import CA: %v", got["talos"])
	}
}

// The pre-class chart renders no label or taint hints for the autoscaler; the
// labels and taints themselves are part of the compared MachineDeployment spec
// and bootstrap template.
func TestRenderProofSkipsAutoscalerLabelAndTaintHints(t *testing.T) {
	md := func(annotations string) []byte {
		return []byte("apiVersion: cluster.x-k8s.io/v1beta1\nkind: MachineDeployment\nmetadata:\n  name: pool\n  annotations:\n    capacity.cluster-autoscaler.kubernetes.io/memory: 3Gi\n" + annotations + "spec: {replicas: 1}\n---\napiVersion: infrastructure.cluster.x-k8s.io/v1alpha1\nkind: KubevirtMachineTemplate\nmetadata:\n  name: vm-a\nspec: {}\n")
	}
	current := md("    capacity.cluster-autoscaler.kubernetes.io/labels: node-role.kubernetes.io/ingress-nginx=\n    capacity.cluster-autoscaler.kubernetes.io/taints: dedicated=gpu:NoSchedule\n")
	if err := VerifyRendered(current, md("")); err != nil {
		t.Fatalf("label and taint hints failed the proof: %v", err)
	}
	if err := VerifyRendered(current, []byte(strings.Replace(string(md("")), "3Gi", "4Gi", 1))); err == nil {
		t.Fatal("a changed memory capacity passed the proof")
	}
}
