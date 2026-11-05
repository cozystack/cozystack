package restore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"github.com/cozystack/cozystack/internal/kubernetesworker/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Prepare materializes the effective Talos settings for an older classless
// chart. It does not write to the cluster or assume the old render is equal.
func Prepare(class *workers.WorkerClassSpec, values map[string]any) (map[string]any, error) {
	name, err := config.ClassName(values)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("pool is already legacy")
	}
	resolved, err := config.Resolve(class, values)
	if err != nil {
		return nil, err
	}
	if resolved.Bootstrap != workers.BootstrapTalos {
		return nil, fmt.Errorf("bootstrap kairos pools must be removed before rollback; they cannot be restored as Talos")
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out["talos"] = resolved.Talos
	out["osImage"] = resolved.OSImage
	out["kernelModules"] = resolved.KernelModules
	if resolved.Source == "proxmox" {
		proxmox, _ := out["proxmox"].(map[string]any)
		if proxmox == nil {
			proxmox = map[string]any{}
		}
		proxmox["templateTags"] = resolved.TemplateTags
		out["proxmox"] = proxmox
	}
	delete(out, config.ClassField)
	return out, nil
}

// VerifyRendered checks the desired worker specs and reference names, ignoring
// chart-version labels, Job execution images and the class's MD annotation.
func VerifyRendered(current, legacy []byte) error {
	left, err := workerSpecs(current)
	if err != nil {
		return err
	}
	right, err := workerSpecs(legacy)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(left, right) {
		return fmt.Errorf("old-chart restoration changes worker specs or reference identities; refusing to write")
	}
	return nil
}

func workerSpecs(raw []byte) (map[string]any, error) {
	result := map[string]any{}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	deployments, machines := 0, 0
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		var field string
		switch obj.GetKind() {
		case "MachineDeployment":
			deployments++
			field = "spec"
		case "KubevirtMachineTemplate", "ProxmoxMachineTemplate":
			machines++
			field = "spec"
		case "MachineHealthCheck", "WorkloadMonitor":
			field = "spec"
		case "ConfigMap":
			field = "data"
		default:
			continue
		}
		key := obj.GetKind() + "/" + obj.GetNamespace() + "/" + obj.GetName()
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate rendered identity %s", key)
		}
		result[key] = obj.Object[field]
	}
	if deployments != 1 || machines == 0 {
		return nil, fmt.Errorf("render proof requires one MachineDeployment and infrastructure templates, got %d and %d", deployments, machines)
	}
	return result, nil
}
