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

// Package apitest checks the shipped GPUClass CRD the way the apiserver
// applies it, so a marker that silently failed to generate shows up as a
// test failure rather than as an admitted bad object.
package apitest

import (
	"context"
	"os"
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

const crdFile = "../../../packages/system/cozystack-controller/definitions/cozystack.io_gpuclasses.yaml"

func readCRD(t *testing.T) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(crdFile)
	if err != nil {
		t.Fatalf("read GPUClass CRD: %v", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("unmarshal GPUClass CRD: %v", err)
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

func newAdmission(t *testing.T) *admission {
	t.Helper()
	props := v1alpha1(t, readCRD(t)).Schema.OpenAPIV3Schema
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

// expect fails unless errs is empty for wantMsg "" and otherwise holds
// wantMsg, so a test never passes on a refusal for another reason.
func expect(t *testing.T, errs field.ErrorList, wantMsg string) {
	t.Helper()
	if wantMsg == "" {
		if len(errs) > 0 {
			t.Fatalf("refused: %v", errs)
		}
		return
	}
	if len(errs) == 0 {
		t.Fatalf("admitted, want a refusal with %q", wantMsg)
	}
	if !strings.Contains(errs.ToAggregate().Error(), wantMsg) {
		t.Errorf("refused for another reason, want %q: %v", wantMsg, errs)
	}
}

func TestCRDPassesInstallTimeValidation(t *testing.T) {
	crd := readCRD(t)
	crd.Status.StoredVersions = []string{"v1alpha1"}
	var internal apiextensions.CustomResourceDefinition
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(crd, &internal, nil); err != nil {
		t.Fatalf("convert CRD: %v", err)
	}
	for _, e := range apiextvalidation.ValidateCustomResourceDefinition(context.TODO(), &internal) {
		t.Errorf("apiserver would reject this CRD: %v", e)
	}
}

func TestCRDShape(t *testing.T) {
	crd := readCRD(t)
	if crd.Name != "gpuclasses.cozystack.io" || crd.Spec.Group != "cozystack.io" {
		t.Errorf("CRD %s in group %s, want gpuclasses.cozystack.io in cozystack.io", crd.Name, crd.Spec.Group)
	}
	if crd.Spec.Scope != apiextensionsv1.ClusterScoped {
		t.Errorf("scope %s, want Cluster", crd.Spec.Scope)
	}
	// The CRD renders as an ordinary chart template, so without the keep
	// policy a controller uninstall deletes every class with it.
	if got := crd.Annotations["helm.sh/resource-policy"]; got != "keep" {
		t.Errorf("helm.sh/resource-policy = %q, want keep", got)
	}
	if v := v1alpha1(t, crd); v.Subresources == nil || v.Subresources.Status == nil {
		t.Error("no status subresource")
	}
}

func gpuClass(name, spec string) string {
	return "apiVersion: cozystack.io/v1alpha1\nkind: GPUClass\nmetadata:\n  name: " + name + "\nspec:\n" + spec
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
  source:
    devicePlugin:
      resourceName: nvidia.com/NVIDIA_L40S-24C
`

// subdomain returns a DNS subdomain of n characters made of labels of at
// most 63.
func subdomain(n int) string {
	var labels []string
	for n > 0 {
		l := min(n, 63)
		if n > 63 && n-63 < 2 {
			l = n - 2
		}
		labels = append(labels, strings.Repeat("a", l))
		n -= l + 1
	}
	return strings.Join(labels, ".")
}

func withResourceName(name string) string {
	return "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source:\n    devicePlugin:\n      resourceName: " + name + "\n"
}

func TestGPUClassCreate(t *testing.T) {
	a := newAdmission(t)
	tests := []struct {
		name, obj, wantMsg string
	}{
		{name: "passthrough class", obj: gpuClass("h100", passthroughSpec)},
		{name: "vgpu class with profile", obj: gpuClass("l40s-24c", vgpuSpec)},
		{name: "name total is an ordinary class name", obj: gpuClass("total", passthroughSpec)},
		{name: "name with a dot is not a DNS label", obj: gpuClass("h100.whole", passthroughSpec), wantMsg: "GPUClass name must be a DNS label"},
		{name: "upper-case name", obj: gpuClass("H100", passthroughSpec), wantMsg: "GPUClass name must be a DNS label"},
		{name: "name longer than 63", obj: gpuClass("a"+strings.Repeat("b", 63), passthroughSpec), wantMsg: "GPUClass name must be a DNS label"},
		{name: "name of exactly 63", obj: gpuClass("a"+strings.Repeat("b", 62), passthroughSpec)},
		{name: "vgpu without profile", obj: gpuClass("x", strings.Replace(vgpuSpec, "  profile: L40S-24C\n", "", 1)), wantMsg: "profile is required for mode vgpu"},
		{name: "passthrough with profile", obj: gpuClass("x", passthroughSpec+"  profile: H100-80C\n"), wantMsg: "profile is required for mode vgpu and not allowed for mode passthrough"},
		{name: "unknown mode", obj: gpuClass("x", strings.Replace(passthroughSpec, "passthrough", "mig", 1)), wantMsg: `supported values: "passthrough", "vgpu"`},
		{name: "maxCount 0", obj: gpuClass("x", passthroughSpec+"  maxCount: 0\n"), wantMsg: "spec.maxCount"},
		{name: "maxCount 1", obj: gpuClass("x", passthroughSpec+"  maxCount: 1\n")},
		{name: "memory 80Gi", obj: gpuClass("x", passthroughSpec+"  memory: 80Gi\n")},
		{name: "memory as an integer", obj: gpuClass("x", passthroughSpec+"  memory: 85899345920\n")},
		{name: "memory 0", obj: gpuClass("x", passthroughSpec+"  memory: \"0\"\n"), wantMsg: "memory must be positive"},
		{name: "memory 0 as an integer", obj: gpuClass("x", passthroughSpec+"  memory: 0\n"), wantMsg: "memory must be positive"},
		{name: "negative memory", obj: gpuClass("x", passthroughSpec+"  memory: -1Gi\n"), wantMsg: "memory must be positive"},
		{name: "no vendor", obj: gpuClass("x", strings.Replace(passthroughSpec, "  vendor: nvidia\n", "", 1)), wantMsg: "spec.vendor"},
		{name: "no source", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n"), wantMsg: "spec.source"},
		{name: "empty source", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source: {}\n"), wantMsg: "exactly one source must be set"},
		{
			// dra is not in the schema, so pruning drops it and the union
			// rule sees no member.
			name:    "reserved dra source",
			obj:     gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source:\n    dra:\n      deviceClassName: gpu.nvidia.com\n"),
			wantMsg: "exactly one source must be set",
		},
		{name: "devicePlugin without resourceName", obj: gpuClass("x", "  vendor: nvidia\n  model: H100\n  mode: passthrough\n  source:\n    devicePlugin: {}\n"), wantMsg: "spec.source.devicePlugin.resourceName"},
		{name: "resourceName without a domain", obj: gpuClass("x", withResourceName("gpu")), wantMsg: "spec.source.devicePlugin.resourceName"},
		{name: "resourceName domain not a DNS subdomain", obj: gpuClass("x", withResourceName("NVIDIA.com/gpu")), wantMsg: "spec.source.devicePlugin.resourceName"},
		{name: "resourceName equal to the all-class budget key", obj: gpuClass("x", withResourceName("gpuclass.cozystack.io/devices")), wantMsg: "resourceName must not be in gpuclass.cozystack.io"},
		{name: "resourceName equal to a class budget key", obj: gpuClass("x", withResourceName("h100.gpuclass.cozystack.io/devices")), wantMsg: "resourceName must not be in gpuclass.cozystack.io"},
		{name: "resourceName in a domain that only ends like the budget domain", obj: gpuClass("x", withResourceName("xgpuclass.cozystack.io/devices"))},
		{name: "resourceName name part longer than 63", obj: gpuClass("x", withResourceName("nvidia.com/"+strings.Repeat("a", 64))), wantMsg: "spec.source.devicePlugin.resourceName"},
		{name: "resourceName name part of exactly 63", obj: gpuClass("x", withResourceName("nvidia.com/"+strings.Repeat("a", 63)))},
		// Kubernetes bounds a subdomain's length, not each label's.
		{name: "resourceName domain label longer than 63", obj: gpuClass("x", withResourceName(strings.Repeat("a", 64)+".com/gpu"))},
		// Kubernetes sizes requests.<resourceName> as a qualified name, so
		// the domain gets 253 minus the length of "requests.".
		{name: "resourceName domain of exactly 244", obj: gpuClass("x", withResourceName(subdomain(244)+"/gpu"))},
		{name: "resourceName domain longer than 244", obj: gpuClass("x", withResourceName(subdomain(245)+"/gpu")), wantMsg: "resourceName must be an extended resource name"},
		{name: "resourceName with the requests. prefix", obj: gpuClass("x", withResourceName("requests.nvidia.com/gpu")), wantMsg: "resourceName must be an extended resource name"},
		{name: "resourceName in kubernetes.io", obj: gpuClass("x", withResourceName("kubernetes.io/gpu")), wantMsg: "resourceName must be an extended resource name"},
		{name: "resourceName in a kubernetes.io subdomain", obj: gpuClass("x", withResourceName("gpu.kubernetes.io/gpu")), wantMsg: "resourceName must be an extended resource name"},
		// Kubernetes treats any name containing kubernetes.io/ as native,
		// whatever the label boundary.
		{name: "resourceName in a domain ending in kubernetes.io", obj: gpuClass("x", withResourceName("mykubernetes.io/gpu")), wantMsg: "resourceName must be an extended resource name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, tc.obj, "")
			expect(t, errs, tc.wantMsg)
		})
	}
}

// A class is its resource name and the nodes that advertise it, and
// holding is step 2, so neither field survives a write.
func TestGPUClassPrunesNodeSelectorAndOnCordon(t *testing.T) {
	obj, errs := newAdmission(t).admit(t, gpuClass("h100", passthroughSpec+"  nodeSelector:\n    gpu: h100\n  onCordon: Stay\n"), "")
	expect(t, errs, "")
	spec := obj["spec"].(map[string]any)
	for _, f := range []string{"nodeSelector", "onCordon"} {
		if v, ok := spec[f]; ok {
			t.Errorf("spec.%s = %v survived pruning", f, v)
		}
	}
}

// maxCount has no schema default: its default depends on mode, which a
// structural default cannot express.
func TestGPUClassMaxCountHasNoSchemaDefault(t *testing.T) {
	obj, errs := newAdmission(t).admit(t, gpuClass("l40s", strings.Replace(vgpuSpec, "  maxCount: 2\n", "", 1)), "")
	expect(t, errs, "")
	if v, ok := obj["spec"].(map[string]any)["maxCount"]; ok {
		t.Errorf("maxCount defaulted to %v by the schema", v)
	}
}

func TestGPUClassLicense(t *testing.T) {
	a := newAdmission(t)
	tests := []struct {
		name, spec, wantMsg string
	}{
		{name: "on a vgpu class", spec: vgpuSpec + "  license:\n    secretName: nvidia-license-token\n"},
		{name: "featureType 4", spec: vgpuSpec + "  license:\n    secretName: nvidia-license-token\n    featureType: 4\n"},
		{name: "featureType 2", spec: vgpuSpec + "  license:\n    secretName: nvidia-license-token\n    featureType: 2\n", wantMsg: "supported values: \"1\", \"4\""},
		{name: "on a passthrough class", spec: passthroughSpec + "  license:\n    secretName: nvidia-license-token\n", wantMsg: "license is allowed only for mode vgpu"},
		{name: "without secretName", spec: vgpuSpec + "  license:\n    featureType: 4\n", wantMsg: "spec.license.secretName"},
		{name: "secretName not a DNS subdomain", spec: vgpuSpec + "  license:\n    secretName: NVIDIA_token\n", wantMsg: "spec.license.secretName"},
		{name: "secretName with dots", spec: vgpuSpec + "  license:\n    secretName: nvidia.license.token\n"},
		{name: "secretName of exactly 253", spec: vgpuSpec + "  license:\n    secretName: " + subdomain(253) + "\n"},
		{name: "secretName longer than 253", spec: vgpuSpec + "  license:\n    secretName: " + subdomain(254) + "\n", wantMsg: "spec.license.secretName"},
		{name: "secretName label longer than 63", spec: vgpuSpec + "  license:\n    secretName: " + strings.Repeat("a", 64) + ".token\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, gpuClass("l40s-24c", tc.spec), "")
			expect(t, errs, tc.wantMsg)
		})
	}
}

func TestGPUClassLicenseFeatureTypeDefaultsTo1(t *testing.T) {
	obj, errs := newAdmission(t).admit(t, gpuClass("l40s-24c", vgpuSpec+"  license:\n    secretName: nvidia-license-token\n"), "")
	expect(t, errs, "")
	license := obj["spec"].(map[string]any)["license"].(map[string]any)
	if got := license["featureType"]; got != int64(1) {
		t.Errorf("featureType = %#v, want 1", got)
	}
}

// A class name stands for one offer for the class's lifetime, so what
// makes the offer never changes; a different offer is a new class.
func TestGPUClassOfferIsImmutable(t *testing.T) {
	a := newAdmission(t)
	tests := []struct {
		name, old, obj, wantMsg string
	}{
		{"vendor", gpuClass("x", vgpuSpec), gpuClass("x", strings.Replace(vgpuSpec, "vendor: nvidia", "vendor: amd", 1)), "vendor is immutable"},
		{"model", gpuClass("x", vgpuSpec), gpuClass("x", strings.Replace(vgpuSpec, "model: L40S", "model: L40", 1)), "model is immutable"},
		{"mode passthrough to vgpu", gpuClass("x", passthroughSpec), gpuClass("x", strings.Replace(passthroughSpec, "mode: passthrough", "mode: vgpu\n  profile: H100-80C", 1)), "mode is immutable"},
		{"mode vgpu to passthrough", gpuClass("x", vgpuSpec), gpuClass("x", strings.Replace(strings.Replace(vgpuSpec, "mode: vgpu", "mode: passthrough", 1), "  profile: L40S-24C\n", "", 1)), "mode is immutable"},
		{"profile", gpuClass("x", vgpuSpec), gpuClass("x", strings.Replace(vgpuSpec, "L40S-24C\n", "L40S-48C\n", 1)), "profile is immutable"},
		{"memory changed", gpuClass("x", vgpuSpec), gpuClass("x", strings.Replace(vgpuSpec, "24Gi", "48Gi", 1)), "memory is immutable"},
		{"memory added", gpuClass("x", passthroughSpec), gpuClass("x", passthroughSpec+"  memory: 80Gi\n"), "memory is immutable"},
		{"memory removed", gpuClass("x", vgpuSpec), gpuClass("x", strings.Replace(vgpuSpec, "  memory: 24Gi\n", "", 1)), "memory is immutable"},
		{"memory changed from an integer", gpuClass("x", strings.Replace(vgpuSpec, "24Gi", "25769803776", 1)), gpuClass("x", strings.Replace(vgpuSpec, "24Gi", "48Gi", 1)), "memory is immutable"},
		{
			// A future source kind is pruned by this version, so a class
			// stored with one reads as an empty source here.
			"source kind", gpuClass("x", strings.Replace(passthroughSpec, "  source:\n    devicePlugin:\n      resourceName: nvidia.com/GH100_H100\n", "  source: {}\n", 1)),
			gpuClass("x", passthroughSpec), "source kind is immutable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, tc.obj, "")
			expect(t, errs, "")
			_, errs = a.admit(t, tc.obj, tc.old)
			expect(t, errs, tc.wantMsg)
		})
	}
}

func TestGPUClassUpdatesThatKeepTheOffer(t *testing.T) {
	a := newAdmission(t)
	old := gpuClass("l40s-24c", vgpuSpec+"  license:\n    secretName: nvidia-license-token\n")
	tests := []struct{ name, obj string }{
		{"displayName", gpuClass("l40s-24c", vgpuSpec+"  displayName: NVIDIA L40S 24C\n  license:\n    secretName: nvidia-license-token\n")},
		{"maxCount", gpuClass("l40s-24c", strings.Replace(vgpuSpec, "maxCount: 2", "maxCount: 1", 1)+"  license:\n    secretName: nvidia-license-token\n")},
		{"resourceName", gpuClass("l40s-24c", strings.Replace(vgpuSpec, "nvidia.com/NVIDIA_L40S-24C", "nvidia.com/L40S-24C", 1)+"  license:\n    secretName: nvidia-license-token\n")},
		{"tolerations", gpuClass("l40s-24c", vgpuSpec+tolerationsSpec+"  license:\n    secretName: nvidia-license-token\n")},
		{"license changed", gpuClass("l40s-24c", vgpuSpec+"  license:\n    secretName: nvidia-license-token-2\n    featureType: 4\n")},
		{"license removed", gpuClass("l40s-24c", vgpuSpec)},
		{"memory as written", old},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, tc.obj, old)
			expect(t, errs, "")
		})
	}
}

// A Go client writes a Quantity back in its canonical form, so an update
// that only re-spells the same memory must pass.
func TestGPUClassMemoryComparesAmounts(t *testing.T) {
	a := newAdmission(t)
	tests := []struct{ name, old, obj string }{
		{"binary units", "24576Mi", "24Gi"},
		{"integer to binary units", "25769803776", "24Gi"},
		{"integer to quoted integer", "25769803776", "\"25769803776\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := gpuClass("x", strings.Replace(vgpuSpec, "24Gi", tc.old, 1))
			obj := gpuClass("x", strings.Replace(vgpuSpec, "24Gi", tc.obj, 1))
			_, errs := a.admit(t, obj, old)
			expect(t, errs, "")
		})
	}
}

const tolerationsSpec = `  tolerations:
  - key: nvidia.com/gpu
    operator: Exists
    effect: NoSchedule
  - key: example.com/dedicated
    operator: Equal
    value: gpu
`

func TestGPUClassTolerationsSurvivePruning(t *testing.T) {
	obj, errs := newAdmission(t).admit(t, gpuClass("h100", passthroughSpec+tolerationsSpec), "")
	expect(t, errs, "")
	tols, _ := obj["spec"].(map[string]any)["tolerations"].([]any)
	if len(tols) != 2 {
		t.Fatalf("tolerations = %#v, want both entries", tols)
	}
	if got := tols[0].(map[string]any)["effect"]; got != "NoSchedule" {
		t.Errorf("tolerations[0].effect = %v, want NoSchedule", got)
	}
	if got := tols[1].(map[string]any)["value"]; got != "gpu" {
		t.Errorf("tolerations[1].value = %v, want gpu", got)
	}
}

// The launcher pod webhook copies a class's tolerations onto the pod, so
// the class refuses every toleration pod validation refuses: Gt and Lt
// where their feature gate is off, and the rules of ValidateTolerations in
// k8s.io/kubernetes pkg/apis/core/validation.
func TestGPUClassTolerations(t *testing.T) {
	a := newAdmission(t)
	tests := []struct {
		name string
		// toleration is one list item, its first line without the dash.
		toleration string
		wantMsg    string
	}{
		{name: "Equal", toleration: "key: example.com/gpu-gen\n    operator: Equal\n    value: \"5\"\n    effect: NoSchedule"},
		{name: "no operator", toleration: "key: example.com/dedicated\n    value: gpu"},
		{name: "Exists without value", toleration: "key: nvidia.com/gpu\n    operator: Exists\n    effect: NoSchedule"},
		{name: "empty key with Exists", toleration: "operator: Exists"},
		{name: "explicit empty key with Exists", toleration: "key: \"\"\n    operator: Exists"},
		{name: "explicit empty key with Equal", toleration: "key: \"\"\n    operator: Equal\n    value: gpu", wantMsg: "a toleration with an empty key needs operator Exists"},
		{name: "NoExecute with tolerationSeconds", toleration: "key: example.com/maintenance\n    operator: Exists\n    effect: NoExecute\n    tolerationSeconds: 300"},
		// Pod validation takes a negative value and the taint manager evicts
		// at once, so the class takes it too.
		{name: "negative tolerationSeconds", toleration: "key: example.com/maintenance\n    operator: Exists\n    effect: NoExecute\n    tolerationSeconds: -1"},
		{name: "PreferNoSchedule", toleration: "key: example.com/soft\n    operator: Exists\n    effect: PreferNoSchedule"},
		{name: "key without a prefix", toleration: "key: dedicated\n    operator: Exists"},
		{name: "empty value with Equal", toleration: "key: dedicated\n    operator: Equal\n    value: \"\""},
		{name: "Gt", toleration: "key: example.com/gpu-gen\n    operator: Gt\n    value: \"3\"", wantMsg: `supported values: "Exists", "Equal"`},
		{name: "Lt", toleration: "key: example.com/gpu-gen\n    operator: Lt\n    value: \"9\"", wantMsg: `supported values: "Exists", "Equal"`},
		{name: "empty key with Equal", toleration: "operator: Equal\n    value: gpu", wantMsg: "a toleration with an empty key needs operator Exists"},
		{name: "empty key without operator", toleration: "effect: NoSchedule", wantMsg: "a toleration with an empty key needs operator Exists"},
		{name: "Exists with a value", toleration: "key: nvidia.com/gpu\n    operator: Exists\n    value: present", wantMsg: "a toleration with operator Exists takes no value"},
		{name: "tolerationSeconds on NoSchedule", toleration: "key: nvidia.com/gpu\n    operator: Exists\n    effect: NoSchedule\n    tolerationSeconds: 300", wantMsg: "tolerationSeconds needs effect NoExecute"},
		{name: "tolerationSeconds without effect", toleration: "key: nvidia.com/gpu\n    operator: Exists\n    tolerationSeconds: 300", wantMsg: "tolerationSeconds needs effect NoExecute"},
		{name: "unknown effect", toleration: "key: nvidia.com/gpu\n    operator: Exists\n    effect: Bogus", wantMsg: `supported values: "NoSchedule", "PreferNoSchedule", "NoExecute"`},
		{name: "key not a qualified name", toleration: "key: not a key!\n    operator: Exists", wantMsg: "spec.tolerations[0].key"},
		{name: "key prefix not a subdomain", toleration: "key: Example.COM/gpu\n    operator: Exists", wantMsg: "spec.tolerations[0].key"},
		{name: "key name longer than 63", toleration: "key: example.com/" + strings.Repeat("a", 64) + "\n    operator: Exists", wantMsg: "spec.tolerations[0].key"},
		{name: "key prefix longer than 253", toleration: "key: " + subdomain(254) + "/gpu\n    operator: Exists", wantMsg: "toleration key prefix must be at most 253 characters"},
		{name: "key prefix of exactly 253", toleration: "key: " + subdomain(253) + "/gpu\n    operator: Exists"},
		{name: "key prefix label longer than 63", toleration: "key: " + strings.Repeat("a", 64) + ".com/gpu\n    operator: Exists"},
		{name: "value not a label value", toleration: "key: dedicated\n    value: not a value", wantMsg: "spec.tolerations[0].value"},
		{name: "value longer than 63", toleration: "key: dedicated\n    value: " + strings.Repeat("a", 64), wantMsg: "spec.tolerations[0].value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := a.admit(t, gpuClass("h100", passthroughSpec+"  tolerations:\n  - "+tc.toleration+"\n"), "")
			expect(t, errs, tc.wantMsg)
		})
	}
}

func TestGPUClassStatus(t *testing.T) {
	a := newAdmission(t)
	status := func(s string) string { return gpuClass("h100", passthroughSpec) + "status:\n" + s }
	tests := []struct {
		name, status, wantMsg string
	}{
		{name: "free and maxFreePerNode", status: "  free: 12\n  maxFreePerNode: 4\n"},
		{name: "zero free", status: "  free: 0\n  maxFreePerNode: 0\n"},
		{name: "negative free", status: "  free: -1\n", wantMsg: "status.free"},
		{name: "negative maxFreePerNode", status: "  maxFreePerNode: -1\n", wantMsg: "status.maxFreePerNode"},
		{name: "condition", status: "  conditions:\n  - type: ResourceNameConflict\n    status: \"True\"\n    reason: SharedResourceName\n    message: another class has this resource name\n    lastTransitionTime: \"2026-11-03T12:00:00Z\"\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj, errs := a.admit(t, status(tc.status), "")
			expect(t, errs, tc.wantMsg)
			if tc.wantMsg == "" && len(obj["status"].(map[string]any)) == 0 {
				t.Error("status pruned")
			}
		})
	}
}
