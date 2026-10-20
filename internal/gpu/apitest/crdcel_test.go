/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package apitest checks the shipped gpu.cozystack.io CRDs the way the
// apiserver applies them, so a marker that silently failed to generate
// shows up as a test failure rather than as an admitted bad object.
package apitest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	schemacel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

const definitionsDir = "../../../packages/system/cozystack-controller/definitions"

var crdFiles = map[string]string{
	"GPUClass":  "gpu.cozystack.io_gpuclasses.yaml",
	"GPUDevice": "gpu.cozystack.io_gpudevices.yaml",
	"GPUNode":   "gpu.cozystack.io_gpunodes.yaml",
}

func readCRD(t *testing.T, kind string) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(definitionsDir, crdFiles[kind]))
	if err != nil {
		t.Fatalf("read %s CRD: %v", kind, err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("unmarshal %s CRD: %v", kind, err)
	}
	return &crd
}

// v1alpha1 fails the test when the version is missing, so a renamed
// version cannot turn every assertion below into a no-op.
func v1alpha1(t *testing.T, crd *apiextensionsv1.CustomResourceDefinition) *apiextensionsv1.CustomResourceDefinitionVersion {
	t.Helper()
	for i := range crd.Spec.Versions {
		if v := &crd.Spec.Versions[i]; v.Name == "v1alpha1" && v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			return v
		}
	}
	t.Fatalf("%s: no v1alpha1 version with a schema", crd.Name)
	return nil
}

// admission runs the steps the apiserver applies to a custom resource
// write, in its order: prune, default, structural schema, list types, CEL
// rules.
type admission struct {
	structural *schema.Structural
	schema     apiservervalidation.SchemaValidator
	cel        *schemacel.Validator
}

func newAdmission(t *testing.T, kind string) *admission {
	t.Helper()
	props := v1alpha1(t, readCRD(t, kind)).Schema.OpenAPIV3Schema
	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(props, &internal, nil); err != nil {
		t.Fatalf("convert schema: %v", err)
	}
	structural, err := schema.NewStructural(&internal)
	if err != nil {
		t.Fatalf("structural schema: %v", err)
	}
	validator, _, err := apiservervalidation.NewSchemaValidator(&internal)
	if err != nil {
		t.Fatalf("schema validator: %v", err)
	}
	return &admission{
		structural: structural,
		schema:     validator,
		cel:        schemacel.NewValidator(structural, true, celconfig.PerCallLimit),
	}
}

func decode(t *testing.T, doc string) map[string]any {
	t.Helper()
	if doc == "" {
		return nil
	}
	js, err := yaml.YAMLToJSON([]byte(doc))
	if err != nil {
		t.Fatalf("yaml: %v", err)
	}
	var obj map[string]any
	// utiljson decodes integers as int64, as the apiserver does.
	if err := utiljson.Unmarshal(js, &obj); err != nil {
		t.Fatalf("json: %v", err)
	}
	return obj
}

// admit returns the object as stored and the errors the apiserver would
// report. old is empty for a create.
func (a *admission) admit(t *testing.T, doc, old string) (map[string]any, field.ErrorList) {
	t.Helper()
	obj, oldObj := decode(t, doc), decode(t, old)
	for _, o := range []map[string]any{obj, oldObj} {
		if o != nil {
			pruning.Prune(o, a.structural, true)
			defaulting.Default(o, a.structural)
		}
	}
	errs := apiservervalidation.ValidateCustomResource(nil, obj, a.schema)
	errs = append(errs, listtype.ValidateListSetsAndMaps(nil, a.structural, obj)...)
	var oldArg any
	if oldObj != nil {
		oldArg = oldObj
	}
	celErrs, _ := a.cel.Validate(context.TODO(), nil, a.structural, obj, oldArg, celconfig.RuntimeCELCostBudget)
	return obj, append(errs, celErrs...)
}

func TestCRDsPassInstallTimeValidation(t *testing.T) {
	for kind := range crdFiles {
		t.Run(kind, func(t *testing.T) {
			crd := readCRD(t, kind)
			crd.Status.StoredVersions = []string{"v1alpha1"}
			var internal apiextensions.CustomResourceDefinition
			if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(crd, &internal, nil); err != nil {
				t.Fatalf("convert CRD: %v", err)
			}
			for _, e := range apiextvalidation.ValidateCustomResourceDefinition(context.TODO(), &internal) {
				t.Errorf("apiserver would reject this CRD: %v", e)
			}
		})
	}
}

// The CRDs render as ordinary chart templates, so without the keep policy
// a controller uninstall deletes every GPUDevice, its hold and its usage
// record with the CRD.
func TestCRDsAreKeptOnChartUninstall(t *testing.T) {
	for kind := range crdFiles {
		if got := readCRD(t, kind).Annotations["helm.sh/resource-policy"]; got != "keep" {
			t.Errorf("%s: helm.sh/resource-policy = %q, want keep", kind, got)
		}
	}
}

// Tenants can write a GPUDevice but not its status; everything the binder
// decides lives there.
func TestCRDsHaveStatusSubresource(t *testing.T) {
	for kind := range crdFiles {
		v := v1alpha1(t, readCRD(t, kind))
		if v.Subresources == nil || v.Subresources.Status == nil {
			t.Errorf("%s: no status subresource", kind)
		}
	}
}

func TestGPUDeviceNodeNameIsSelectable(t *testing.T) {
	for _, f := range v1alpha1(t, readCRD(t, "GPUDevice")).SelectableFields {
		if f.JSONPath == ".status.nodeName" {
			return
		}
	}
	t.Error("GPUDevice does not declare .status.nodeName in selectableFields")
}

func TestScope(t *testing.T) {
	want := map[string]apiextensionsv1.ResourceScope{
		"GPUClass":  apiextensionsv1.ClusterScoped,
		"GPUDevice": apiextensionsv1.NamespaceScoped,
		"GPUNode":   apiextensionsv1.ClusterScoped,
	}
	for kind, scope := range want {
		if got := readCRD(t, kind).Spec.Scope; got != scope {
			t.Errorf("%s: scope %s, want %s", kind, got, scope)
		}
	}
}

func gpuClass(name, spec string) string {
	return "apiVersion: gpu.cozystack.io/v1alpha1\nkind: GPUClass\nmetadata:\n  name: " + name + "\nspec:\n" + spec
}

const passthroughSpec = `  vendor: nvidia
  model: H100
  mode: passthrough
  source:
    devicePlugin:
      resourceName: nvidia.com/GH100_H100
`

const vgpuSpec = `  vendor: nvidia
  model: L40S
  mode: vgpu
  profile: L40S-24C
  maxCount: 2
  memory: 24Gi
  nodeSelector:
    gpu.cozystack.io/10de-26b9: L40S-24C
  source:
    devicePlugin:
      resourceName: nvidia.com/NVIDIA_L40S-24C
`

func TestGPUClassCreate(t *testing.T) {
	a := newAdmission(t, "GPUClass")
	tests := []struct {
		name       string
		obj        string
		wantReject bool
	}{
		{name: "passthrough class", obj: gpuClass("h100", passthroughSpec)},
		{name: "vgpu class with profile", obj: gpuClass("l40s-24c", vgpuSpec)},
		{name: "name total is reserved", obj: gpuClass("total", passthroughSpec), wantReject: true},
		{name: "name with a dot is not a DNS label", obj: gpuClass("h100.whole", passthroughSpec), wantReject: true},
		{name: "upper-case name", obj: gpuClass("H100", passthroughSpec), wantReject: true},
		{name: "name longer than 63", obj: gpuClass("a234567890123456789012345678901234567890123456789012345678901234", passthroughSpec), wantReject: true},
		{name: "name of exactly 63", obj: gpuClass("a23456789012345678901234567890123456789012345678901234567890123", passthroughSpec)},
		{name: "vgpu without profile", obj: gpuClass("x", `  vendor: nvidia
  model: L40S
  mode: vgpu
  source:
    devicePlugin:
      resourceName: nvidia.com/NVIDIA_L40S-24C
`), wantReject: true},
		{name: "passthrough with profile", obj: gpuClass("x", passthroughSpec+"  profile: H100-80C\n"), wantReject: true},
		{name: "unknown mode", obj: gpuClass("x", `  vendor: nvidia
  model: H100
  mode: mig
  source:
    devicePlugin:
      resourceName: nvidia.com/GH100_H100
`), wantReject: true},
		{name: "maxCount 0", obj: gpuClass("x", passthroughSpec+"  maxCount: 0\n"), wantReject: true},
		{name: "maxCount 1", obj: gpuClass("x", passthroughSpec+"  maxCount: 1\n")},
		{name: "onCordon Stay", obj: gpuClass("x", passthroughSpec+"  onCordon: Stay\n")},
		{name: "unknown onCordon", obj: gpuClass("x", passthroughSpec+"  onCordon: Drain\n"), wantReject: true},
		{name: "no source", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n"), wantReject: true},
		{name: "empty source", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source: {}\n"), wantReject: true},
		{
			// dra is not in the schema, so pruning drops it and the
			// union rule sees no member.
			name: "reserved dra source",
			obj: gpuClass("x", `  vendor: nvidia
  model: H100
  mode: passthrough
  source:
    dra:
      deviceClassName: gpu.nvidia.com
`),
			wantReject: true,
		},
		{name: "devicePlugin without resourceName", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source:\n    devicePlugin: {}\n"), wantReject: true},
		{name: "resourceName without a domain", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source:\n    devicePlugin:\n      resourceName: gpu\n"), wantReject: true},
		{name: "resourceName in the budget key domain", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source:\n    devicePlugin:\n      resourceName: gpu.cozystack.io/h100\n"), wantReject: true},
		{name: "resourceName name part longer than 63", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source:\n    devicePlugin:\n      resourceName: nvidia.com/"+strings.Repeat("a", 64)+"\n"), wantReject: true},
		{name: "resourceName name part of exactly 63", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source:\n    devicePlugin:\n      resourceName: nvidia.com/"+strings.Repeat("a", 63)+"\n")},
		{name: "no vendor", obj: gpuClass("x", "  model: H100\n  mode: passthrough\n  source:\n    devicePlugin:\n      resourceName: nvidia.com/GH100_H100\n"), wantReject: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, tc.obj, "")
			if got := len(errs) > 0; got != tc.wantReject {
				t.Errorf("rejected = %v, want %v; errors: %v", got, tc.wantReject, errs)
			}
		})
	}
}

func TestGPUClassOnCordonDefaultsToMove(t *testing.T) {
	obj, errs := newAdmission(t, "GPUClass").admit(t, gpuClass("h100", passthroughSpec), "")
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := obj["spec"].(map[string]any)["onCordon"]; got != "Move" {
		t.Errorf("onCordon = %v, want Move", got)
	}
}

// maxCount has no schema default: its default depends on mode, which a
// structural default cannot express.
func TestGPUClassMaxCountHasNoSchemaDefault(t *testing.T) {
	obj, errs := newAdmission(t, "GPUClass").admit(t, gpuClass("l40s", `  vendor: nvidia
  model: L40S
  mode: vgpu
  profile: L40S-24C
  source:
    devicePlugin:
      resourceName: nvidia.com/NVIDIA_L40S-24C
`), "")
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if v, ok := obj["spec"].(map[string]any)["maxCount"]; ok {
		t.Errorf("maxCount defaulted to %v by the schema", v)
	}
}

// The CRD carries no transition rule on GPUClass: onCordon, resourceName
// and nodeSelector stay changeable, and freezing the rest while the
// finalizer is present needs the finalizer, which CRD CEL cannot read.
func TestGPUClassUpdateIsNotFrozenByTheCRD(t *testing.T) {
	a := newAdmission(t, "GPUClass")
	old := gpuClass("l40s-24c", vgpuSpec)
	tests := []struct{ name, obj string }{
		{"onCordon", gpuClass("l40s-24c", vgpuSpec+"  onCordon: Stay\n")},
		{"resourceName", gpuClass("l40s-24c", `  vendor: nvidia
  model: L40S
  mode: vgpu
  profile: L40S-24C
  maxCount: 2
  memory: 24Gi
  nodeSelector:
    gpu.cozystack.io/10de-26b9: L40S-24C
  source:
    devicePlugin:
      resourceName: nvidia.com/L40S-24C
`)},
		{"nodeSelector", gpuClass("l40s-24c", `  vendor: nvidia
  model: L40S
  mode: vgpu
  profile: L40S-24C
  maxCount: 2
  memory: 24Gi
  nodeSelector:
    pool: b
  source:
    devicePlugin:
      resourceName: nvidia.com/NVIDIA_L40S-24C
`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, errs := a.admit(t, tc.obj, old); len(errs) > 0 {
				t.Errorf("update refused: %v", errs)
			}
		})
	}
}

func gpuDevice(spec string) string {
	return "apiVersion: gpu.cozystack.io/v1alpha1\nkind: GPUDevice\nmetadata:\n  name: train-0\n  namespace: tenant-acme\nspec:\n" + spec
}

func TestGPUDeviceCreate(t *testing.T) {
	a := newAdmission(t, "GPUDevice")
	tests := []struct {
		name       string
		spec       string
		wantReject bool
	}{
		{name: "count 1", spec: "  className: l40s-24c\n  count: 1\n"},
		{name: "count 4", spec: "  className: h100\n  count: 4\n"},
		{name: "count 0", spec: "  className: h100\n  count: 0\n", wantReject: true},
		{name: "negative count", spec: "  className: h100\n  count: -1\n", wantReject: true},
		{name: "no count", spec: "  className: h100\n", wantReject: true},
		{name: "no className", spec: "  count: 1\n", wantReject: true},
		{name: "className not a DNS label", spec: "  className: H100.whole\n  count: 1\n", wantReject: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, gpuDevice(tc.spec), "")
			if got := len(errs) > 0; got != tc.wantReject {
				t.Errorf("rejected = %v, want %v; errors: %v", got, tc.wantReject, errs)
			}
		})
	}
}

func TestGPUDeviceSpecIsImmutable(t *testing.T) {
	a := newAdmission(t, "GPUDevice")
	old := gpuDevice("  className: l40s-24c\n  count: 1\n")
	tests := []struct {
		name       string
		obj        string
		wantReject bool
	}{
		{name: "count changed", obj: gpuDevice("  className: l40s-24c\n  count: 2\n"), wantReject: true},
		{name: "className changed", obj: gpuDevice("  className: h100\n  count: 1\n"), wantReject: true},
		{name: "spec unchanged, status written", obj: gpuDevice("  className: l40s-24c\n  count: 1\nstatus:\n  phase: Bound\n  nodeName: gpu-node-3\n")},
		{name: "labels changed", obj: "apiVersion: gpu.cozystack.io/v1alpha1\nkind: GPUDevice\nmetadata:\n  name: train-0\n  namespace: tenant-acme\n  labels:\n    pool: inference\nspec:\n  className: l40s-24c\n  count: 1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, tc.obj, old)
			if got := len(errs) > 0; got != tc.wantReject {
				t.Errorf("rejected = %v, want %v; errors: %v", got, tc.wantReject, errs)
			}
		})
	}
}

func TestGPUDeviceStatusPhaseEnum(t *testing.T) {
	a := newAdmission(t, "GPUDevice")
	for phase, wantReject := range map[string]bool{"Pending": false, "Bound": false, "Attached": false, "Lost": false, "Running": true} {
		t.Run(phase, func(t *testing.T) {
			_, errs := a.admit(t, gpuDevice("  className: h100\n  count: 1\nstatus:\n  phase: "+phase+"\n"), "")
			if got := len(errs) > 0; got != wantReject {
				t.Errorf("rejected = %v, want %v; errors: %v", got, wantReject, errs)
			}
		})
	}
}

func TestGPUNodeInventory(t *testing.T) {
	a := newAdmission(t, "GPUNode")
	node := func(devices string) string {
		return "apiVersion: gpu.cozystack.io/v1alpha1\nkind: GPUNode\nmetadata:\n  name: gpu-node-3\nstatus:\n  resourceNames: [nvidia.com/NVIDIA_L40S-24C]\n  devices:\n" + devices
	}
	tests := []struct {
		name       string
		devices    string
		wantReject bool
	}{
		{name: "pf and vf", devices: `  - pciAddress: "0000:41:00.0"
    model: L40S
    function: pf
    vfs: 16
  - pciAddress: "0000:41:00.4"
    function: vf
    profile: L40S-24C
    resourceName: nvidia.com/NVIDIA_L40S-24C
    healthy: true
    usedBy: {namespace: tenant-acme, pod: virt-launcher-vm-instance-trainer-x7k2p}
`},
		{name: "mdev parent with allocatable", devices: "  - pciAddress: \"0000:41:00.0\"\n    function: whole\n    allocatable: 4\n"},
		{name: "zero allocatable", devices: "  - pciAddress: \"0000:41:00.0\"\n    function: whole\n    allocatable: 0\n"},
		{name: "negative allocatable", devices: "  - pciAddress: \"0000:41:00.0\"\n    function: whole\n    allocatable: -1\n", wantReject: true},
		{name: "unknown function", devices: "  - pciAddress: \"0000:41:00.0\"\n    function: mig\n", wantReject: true},
		{name: "malformed PCI address", devices: "  - pciAddress: \"41:00.0\"\n    function: whole\n", wantReject: true},
		{name: "duplicate PCI address", devices: "  - pciAddress: \"0000:41:00.0\"\n    function: whole\n  - pciAddress: \"0000:41:00.0\"\n    function: whole\n", wantReject: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, node(tc.devices), "")
			if got := len(errs) > 0; got != tc.wantReject {
				t.Errorf("rejected = %v, want %v; errors: %v", got, tc.wantReject, errs)
			}
		})
	}
}

func TestGPUNodeAllocatableSurvivesPruning(t *testing.T) {
	obj, errs := newAdmission(t, "GPUNode").admit(t, "apiVersion: gpu.cozystack.io/v1alpha1\nkind: GPUNode\nmetadata:\n  name: gpu-node-3\nstatus:\n  devices:\n  - pciAddress: \"0000:41:00.0\"\n    function: whole\n    allocatable: 4\n", "")
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	dev := obj["status"].(map[string]any)["devices"].([]any)[0].(map[string]any)
	if got := dev["allocatable"]; got != int64(4) {
		t.Errorf("allocatable = %#v, want 4", got)
	}
}
