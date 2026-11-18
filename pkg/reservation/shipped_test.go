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

package reservation

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
)

// The kinds whose consumption has to be declared: the IaaS kinds carry the
// virtualization overhead the reservation exists to keep out of the tenant
// contract.
var declaredKinds = []string{"vm-instance", "vm-disk", "kubernetes", "kubernetes-nodes"}

func shippedDefinitions(t *testing.T) map[string]*cozyv1alpha1.ApplicationDefinition {
	t.Helper()
	files, err := filepath.Glob("../../packages/system/*-rd/cozyrds/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no ApplicationDefinition found under packages/system/*-rd/cozyrds")
	}
	out := make(map[string]*cozyv1alpha1.ApplicationDefinition, len(files))
	from := make(map[string]string, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// Helm renders every document of the file, while a strict decode
		// reads only the first, so a second one would go unchecked.
		if docs := strings.Split(string(raw), "\n---"); len(docs) > 1 {
			for _, d := range docs[1:] {
				if strings.TrimSpace(d) != "" {
					t.Fatalf("%s: holds more than one YAML document", f)
				}
			}
		}
		def := &cozyv1alpha1.ApplicationDefinition{}
		// Strict, so that a misspelled field in a hand-written declaration
		// fails here instead of being pruned by the API server.
		if err := yaml.UnmarshalStrict(raw, def); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		// A later file would replace an earlier one of the same name, and the
		// earlier declaration would then never be compiled.
		if prev, ok := from[def.Name]; ok {
			t.Fatalf("ApplicationDefinition %q is declared in both %s and %s", def.Name, prev, f)
		}
		from[def.Name] = f
		out[def.Name] = def
	}
	return out
}

// presetRow matches one row of the dict literals in _resourcepresets.tpl;
// its fields are then read by key, so that their order does not matter.
var (
	presetRow   = regexp.MustCompile(`"([a-z0-9.]+)"\s+\(dict ([^)]*)\)`)
	presetField = func(key string) *regexp.Regexp { return regexp.MustCompile(`"` + key + `"\s+"([^"]+)"`) }
	presetCPU   = presetField("cpu")
	presetMem   = presetField("memory")
)

// shippedPresets reads the preset table the charts render from, so that a
// declaration is evaluated against the figures a tenant actually gets.
func shippedPresets(t *testing.T) map[string]ResourceList {
	t.Helper()
	raw, err := os.ReadFile("../../packages/library/cozy-lib/templates/_resourcepresets.tpl")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ResourceList{}
	for _, m := range presetRow.FindAllStringSubmatch(string(raw), -1) {
		cpu, mem := presetCPU.FindStringSubmatch(m[2]), presetMem.FindStringSubmatch(m[2])
		if cpu == nil || mem == nil {
			t.Fatalf("preset %q: no cpu or memory in %q", m[1], m[2])
		}
		out[m[1]] = ResourceList{"cpu": resource.MustParse(cpu[1]), "memory": resource.MustParse(mem[1])}
	}
	// Five series of eight sizes and seven legacy aliases. A table whose
	// layout drifts from the pattern above must fail here, not shrink.
	if len(out) != 47 {
		t.Fatalf("parsed %d presets from _resourcepresets.tpl, expected 47", len(out))
	}
	return out
}

func shippedEnvironment(t *testing.T) Environment {
	t.Helper()
	env, err := NewStaticEnvironment(shippedInstancetypes(t), shippedPresets(t))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func compileShipped(t *testing.T, defs map[string]*cozyv1alpha1.ApplicationDefinition, kind string) *Evaluator {
	t.Helper()
	def, ok := defs[kind]
	if !ok {
		t.Fatalf("no ApplicationDefinition named %q", kind)
	}
	schema, err := defaulting.BuildSchema(def.Spec.Application.OpenAPISchema)
	if err != nil {
		t.Fatalf("%s schema: %v", def.Name, err)
	}
	ev, err := Compile(def.Spec.Consumption, schema)
	if err != nil {
		t.Fatalf("compile %s: %v", def.Name, err)
	}
	return ev
}

// Every declaration that ships has to compile against its kind's schema and
// evaluate on the kind's defaults, so a broken one fails here rather than on
// the first order of that kind.
func TestShippedDeclarationsCompile(t *testing.T) {
	env := shippedEnvironment(t)
	defs := shippedDefinitions(t)
	for _, kind := range declaredKinds {
		def, ok := defs[kind]
		if !ok {
			t.Fatalf("no ApplicationDefinition named %q", kind)
		}
		if def.Spec.Consumption == nil {
			t.Errorf("%s: declares neither its consumption nor an exemption", kind)
		}
	}
	for name, def := range defs {
		if def.Spec.Consumption == nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if _, err := compileShipped(t, defs, name).EvaluateJSON([]byte(`{}`), env); err != nil {
				t.Fatalf("evaluate defaults: %v", err)
			}
		})
	}
}

func TestShippedDeclarations(t *testing.T) {
	env := shippedEnvironment(t)
	defs := shippedDefinitions(t)

	cases := []struct {
		kind, name, values string
		want               ResourceList
	}{
		{
			kind: "vm-instance", name: "defaults",
			values: `{}`,
			want:   rl("cpu", "1", "memory", "4Gi"),
		},
		{
			kind: "vm-instance", name: "dedicated type pins the emulator core",
			values: `{"instanceType": "cx1.2xlarge"}`,
			want:   rl("dedicated-cpu", "9", "hugepages-2Mi", "16Gi"),
		},
		{
			kind: "vm-instance", name: "1Gi hugepages type",
			values: `{"instanceType": "m1.large1gi"}`,
			want:   rl("cpu", "2", "hugepages-1Gi", "16Gi"),
		},
		{
			kind: "vm-instance", name: "overcommitted type",
			values: `{"instanceType": "o1.large"}`,
			want:   rl("cpu", "2", "overcommitted-memory", "8Gi"),
		},
		{
			// cpu is cores per socket, and a complete block replaces the
			// instance type.
			kind: "vm-instance", name: "complete resources block",
			values: `{"instanceType": "cx1.2xlarge", "resources": {"cpu": 4, "sockets": 2, "memory": "8Gi"}}`,
			want:   rl("cpu", "8", "memory", "8Gi"),
		},
		{
			kind: "vm-instance", name: "complete resources block as strings",
			values: `{"resources": {"cpu": "4", "sockets": "2", "memory": "8Gi"}}`,
			want:   rl("cpu", "8", "memory", "8Gi"),
		},
		{
			// No instance type to resolve: the block alone sizes the VM.
			kind: "vm-instance", name: "complete resources block without an instance type",
			values: `{"instanceType": "", "resources": {"cpu": 2, "sockets": 1, "memory": "2Gi"}}`,
			want:   rl("cpu", "2", "memory", "2Gi"),
		},
		{
			// The chart decodes cpu and sockets as YAML, so any spelling of a
			// whole number sizes the VM.
			kind: "vm-instance", name: "whole numbers spelled as decimals and exponents",
			values: `{"resources": {"cpu": "2.0", "sockets": "1e1", "memory": "8Gi"}}`,
			want:   rl("cpu", "20", "memory", "8Gi"),
		},
		{
			kind: "vm-instance", name: "explicit plus sign",
			values: `{"resources": {"cpu": "+2", "sockets": 1, "memory": "1Gi"}}`,
			want:   rl("cpu", "2", "memory", "1Gi"),
		},
		{
			kind: "vm-instance", name: "memory at the 1M floor",
			values: `{"resources": {"cpu": 1, "sockets": 1, "memory": "1M"}}`,
			want:   rl("cpu", "1", "memory", "1M"),
		},
		{
			// cpu without sockets emits no topology, so the instance type
			// still sizes the VM.
			kind: "vm-instance", name: "cpu alone leaves the instance type in charge",
			values: `{"instanceType": "u1.small", "resources": {"cpu": 4}}`,
			want:   rl("cpu", "1", "memory", "2Gi"),
		},
		{
			kind: "vm-instance", name: "null cpu and sockets are absent",
			values: `{"instanceType": "u1.small", "resources": {"cpu": null, "sockets": null}}`,
			want:   rl("cpu", "1", "memory", "2Gi"),
		},
		{
			kind: "vm-instance", name: "external",
			values: `{"instanceType": "u1.small", "external": true}`,
			want:   rl("cpu", "1", "memory", "2Gi", "services.loadbalancers", "1"),
		},
		{
			kind: "vm-disk", name: "defaults",
			values: `{}`,
			want:   rl("storage", "5Gi"),
		},
		{
			kind: "vm-disk", name: "declared size",
			values: `{"storage": "100Gi"}`,
			want:   rl("storage", "100Gi"),
		},
		{
			kind: "vm-disk", name: "size in bytes",
			values: `{"storage": 10737418240}`,
			want:   rl("storage", "10Gi"),
		},
		{
			// Two replicas of c1.medium plus three t1.micro, all defaults.
			kind: "kubernetes", name: "defaults",
			values: `{}`,
			want:   rl("cpu", "7", "memory", "5632Mi"),
		},
		{
			// cozy-lib fills the preset per key: cpu from resources, memory
			// from c1.medium.
			kind: "kubernetes", name: "partial override on a preset",
			values: `{"controlPlane": {"replicas": 1, "apiServer": {"resources": {"cpu": "4"}}}}`,
			want:   rl("cpu", "5500m", "memory", "2816Mi"),
		},
		{
			kind: "kubernetes", name: "every component sized",
			values: `{"controlPlane": {"replicas": 3,
				"apiServer": {"resourcesPreset": "u1.small"},
				"controllerManager": {"resourcesPreset": "t1.small"},
				"scheduler": {"resources": {"cpu": 1, "memory": "1Gi"}},
				"konnectivity": {"server": {"resourcesPreset": "t1.nano"}}}}`,
			want: rl("cpu", "9750m", "memory", "17280Mi"),
		},
		{
			// The apiserver Service is published so that Proxmox workers can
			// reach it.
			kind: "kubernetes", name: "proxmox publishes the apiserver",
			values: `{"substrate": "proxmox", "controlPlane": {"replicas": 1}}`,
			want:   rl("cpu", "3500m", "memory", "2816Mi", "services.loadbalancers", "1"),
		},
		{
			// The tenant cluster's cloud-controller-manager realizes the
			// controller's Service as a LoadBalancer in the tenant namespace.
			kind: "kubernetes", name: "ingress exposed by a load balancer",
			values: `{"controlPlane": {"replicas": 1}, "addons": {"ingressNginx": {"enabled": true, "exposeMethod": "LoadBalancer"}}}`,
			want:   rl("cpu", "3500m", "memory", "2816Mi", "services.loadbalancers", "1"),
		},
		{
			kind: "kubernetes", name: "proxied ingress publishes nothing",
			values: `{"controlPlane": {"replicas": 1}, "addons": {"ingressNginx": {"enabled": true, "exposeMethod": "Proxied"}}}`,
			want:   rl("cpu", "3500m", "memory", "2816Mi"),
		},
		{
			// No cloud-controller-manager runs for a Proxmox cluster, so only
			// the apiserver Service is published.
			kind: "kubernetes", name: "proxmox ingress load balancer is not realized here",
			values: `{"substrate": "proxmox", "controlPlane": {"replicas": 1}, "addons": {"ingressNginx": {"enabled": true, "exposeMethod": "LoadBalancer"}}}`,
			want:   rl("cpu", "3500m", "memory", "2816Mi", "services.loadbalancers", "1"),
		},
		{
			kind: "kubernetes", name: "legacy preset alias",
			values: `{"controlPlane": {"replicas": 1, "apiServer": {"resourcesPreset": "medium"}}}`,
			want:   rl("cpu", "2500m", "memory", "1792Mi"),
		},
		{
			// A pool created with defaults reserves its ceiling.
			kind: "kubernetes-nodes", name: "defaults",
			values: `{}`,
			want:   rl("cpu", "10", "memory", "40Gi", "storage", "200Gi"),
		},
		{
			kind: "kubernetes-nodes", name: "sized by resources",
			values: `{"maxReplicas": 3, "instanceType": "cx1.2xlarge", "resources": {"cpu": 2, "memory": "4Gi"}, "diskSize": "30Gi"}`,
			want:   rl("cpu", "6", "memory", "12Gi", "storage", "90Gi"),
		},
		{
			// An unquoted zero is unset to the chart, which then sizes the
			// worker from its instance type.
			kind: "kubernetes-nodes", name: "zero resources leave the instance type in charge",
			values: `{"maxReplicas": 1, "instanceType": "u1.small", "resources": {"cpu": 0, "memory": 0}}`,
			want:   rl("cpu", "1", "memory", "2Gi", "storage", "20Gi"),
		},
		{
			kind: "kubernetes-nodes", name: "dedicated instance type",
			values: `{"maxReplicas": 2, "instanceType": "cx1.2xlarge"}`,
			want:   rl("dedicated-cpu", "18", "hugepages-2Mi", "32Gi", "storage", "40Gi"),
		},
		{
			// CAPI seeds the MachineDeployment from the floor, which nothing
			// keeps at or below the ceiling.
			kind: "kubernetes-nodes", name: "a floor above the ceiling is reserved",
			values: `{"minReplicas": 2, "maxReplicas": 1}`,
			want:   rl("cpu", "2", "memory", "8Gi", "storage", "40Gi"),
		},
		{
			// Proxmox rounds a fractional core up and runs off-cluster.
			kind: "kubernetes-nodes", name: "proxmox accepts millicores",
			values: `{"substrate": "proxmox", "resources": {"cpu": "500m", "memory": "4Gi"}}`,
			want:   rl(),
		},
		{
			kind: "kubernetes-nodes", name: "a pool that cannot grow reserves nothing",
			values: `{"maxReplicas": 0}`,
			want:   rl(),
		},
		{
			// The chart falls back to 20Gi for an empty disk size, as Helm's
			// default does for any empty value.
			kind: "kubernetes-nodes", name: "zero disk size takes the chart's fallback",
			values: `{"maxReplicas": 3, "diskSize": 0}`,
			want:   rl("cpu", "3", "memory", "12Gi", "storage", "60Gi"),
		},
		{
			kind: "kubernetes-nodes", name: "empty disk size takes the chart's fallback",
			values: `{"maxReplicas": 3, "diskSize": ""}`,
			want:   rl("cpu", "3", "memory", "12Gi", "storage", "60Gi"),
		},
		{
			// The chart ignores diskSize on Proxmox, so a value only the
			// schema admits must not fail the reservation.
			kind: "kubernetes-nodes", name: "proxmox ignores an unparseable disk size",
			values: `{"substrate": "proxmox", "diskSize": "1e1.5", "resources": {"cpu": 4, "memory": "8Gi"}}`,
			want:   rl(),
		},
		{
			// Proxmox workers run on an external hypervisor and their disks
			// are cloned there.
			kind: "kubernetes-nodes", name: "proxmox runs outside the cluster",
			values: `{"substrate": "proxmox", "maxReplicas": 5, "resources": {"cpu": 4, "memory": "8Gi"}}`,
			want:   rl(),
		},
	}
	evaluators := map[string]*Evaluator{}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			ev, ok := evaluators[tc.kind]
			if !ok {
				ev = compileShipped(t, defs, tc.kind)
				evaluators[tc.kind] = ev
			}
			got, err := ev.EvaluateJSON([]byte(tc.values), env)
			if err != nil {
				t.Fatal(err)
			}
			assertList(t, tc.name, got, tc.want)
		})
	}
}

// The largest topology the chart renders is past int64 as a vCPU count, so
// the product has to be exact rather than overflow.
func TestShippedVMInstanceLargestTopology(t *testing.T) {
	env := shippedEnvironment(t)
	defs := shippedDefinitions(t)
	got, err := compileShipped(t, defs, "vm-instance").EvaluateJSON([]byte(`{"resources": {"cpu": 4294967295, "sockets": 4294967295, "memory": "1Gi"}}`), env)
	if err != nil {
		t.Fatal(err)
	}
	cpu := got["cpu"]
	if s := cpu.AsDec().String(); s != "18446744065119617025" {
		t.Errorf("cpu = %s, want 18446744065119617025", s)
	}
}

// A figure the declaration cannot resolve is an error, never a smaller one.
func TestShippedDeclarationsFailClosed(t *testing.T) {
	env := shippedEnvironment(t)
	defs := shippedDefinitions(t)

	cases := []struct {
		kind, name, values string
		notFound           bool
	}{
		{kind: "vm-instance", name: "unknown instance type", values: `{"instanceType": "u9.huge"}`, notFound: true},
		{kind: "vm-instance", name: "no sizing at all", values: `{"instanceType": ""}`, notFound: true},
		{kind: "vm-instance", name: "millicore cpu", values: `{"resources": {"cpu": "500m", "sockets": 1, "memory": "1Gi"}}`},
		{kind: "vm-instance", name: "fractional cpu", values: `{"resources": {"cpu": "1.5", "sockets": 1, "memory": "1Gi"}}`},
		{kind: "vm-instance", name: "zero cpu", values: `{"resources": {"cpu": 0, "sockets": 1, "memory": "1Gi"}}`},
		{kind: "vm-instance", name: "zero sockets", values: `{"resources": {"cpu": 2, "sockets": "0", "memory": "1Gi"}}`},
		{kind: "vm-instance", name: "negative cpu", values: `{"resources": {"cpu": -2, "sockets": 1, "memory": "1Gi"}}`},
		{kind: "vm-instance", name: "cpu above uint32", values: `{"resources": {"cpu": 4294967296, "sockets": 1, "memory": "1Gi"}}`},
		{kind: "vm-instance", name: "leading-zero cpu", values: `{"resources": {"cpu": "010", "sockets": 1, "memory": "1Gi"}}`},
		{kind: "vm-instance", name: "invalid cpu beside an instance type", values: `{"instanceType": "u1.small", "resources": {"cpu": "500m"}}`},
		{kind: "vm-instance", name: "memory beside an instance type", values: `{"instanceType": "u1.medium", "resources": {"memory": "8Gi"}}`},
		{kind: "vm-instance", name: "cpu and memory without sockets", values: `{"instanceType": "u1.small", "resources": {"cpu": 4, "memory": "8Gi"}}`},
		{kind: "vm-instance", name: "cpu and sockets without memory", values: `{"instanceType": "u1.small", "resources": {"cpu": 2, "sockets": 1}}`},
		{kind: "vm-instance", name: "zero memory", values: `{"resources": {"cpu": 2, "sockets": 1, "memory": 0}}`},
		{kind: "vm-instance", name: "memory below 1M", values: `{"resources": {"cpu": 2, "sockets": 1, "memory": "999k"}}`},
		{kind: "vm-instance", name: "memory below 1M beside an instance type", values: `{"instanceType": "u1.small", "resources": {"memory": "512k"}}`},
		{kind: "vm-disk", name: "unparseable size", values: `{"storage": "lots"}`},
		{kind: "kubernetes", name: "unknown preset", values: `{"controlPlane": {"apiServer": {"resourcesPreset": "x9.huge"}}}`, notFound: true},
		{kind: "kubernetes-nodes", name: "unknown instance type", values: `{"instanceType": "u9.huge"}`, notFound: true},
		{kind: "kubernetes-nodes", name: "unparseable disk", values: `{"diskSize": "1e1.5"}`},
		{kind: "kubernetes-nodes", name: "cpu without memory", values: `{"maxReplicas": 3, "resources": {"cpu": 2}}`},
		{kind: "kubernetes-nodes", name: "millicore cpu on kubevirt", values: `{"maxReplicas": 3, "resources": {"cpu": "500m", "memory": "4Gi"}}`},
		{kind: "kubernetes-nodes", name: "quoted zero cpu on kubevirt", values: `{"maxReplicas": 3, "resources": {"cpu": "0", "memory": "4Gi"}}`},
		{kind: "kubernetes-nodes", name: "fractional cpu on kubevirt", values: `{"maxReplicas": 3, "resources": {"cpu": "1.5", "memory": "4Gi"}}`},
		{kind: "kubernetes-nodes", name: "memory without cpu", values: `{"maxReplicas": 3, "resources": {"cpu": 0, "memory": "4Gi"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			_, err := compileShipped(t, defs, tc.kind).EvaluateJSON([]byte(tc.values), env)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.notFound && !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got %v", err)
			}
		})
	}
}
