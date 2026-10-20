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
	"sync"
	"testing"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
)

// The declarations below are test fixtures in the shape a kind would carry
// on its ApplicationDefinition. They are evaluated against the kinds' real
// shipped schemas so that defaulting is exercised with the defaults tenants
// actually get.

var postgresDecl = &Declaration{
	Let: []Binding{{
		Name: "count",
		// The autoscaler writes the Cluster's scale subresource, never the
		// application, so the ceiling is what has to be reserved.
		Expression: `values.autoscaling.enabled && values.autoscaling.maxReplicas > values.replicas
			? values.autoscaling.maxReplicas : values.replicas`,
	}},
	Resources: map[string]string{
		// cozy-lib fills a preset per key, so each key is overridden on its own.
		"cpu":     `(has(values.resources.cpu) ? quantity(values.resources.cpu) : preset(values.resourcesPreset).cpu).mul(count)`,
		"memory":  `(has(values.resources.memory) ? quantity(values.resources.memory) : preset(values.resourcesPreset).memory).mul(count)`,
		"storage": `quantity(values.size).mul(count)`,
	},
}

var vmInstanceDecl = &Declaration{
	Let: []Binding{
		// vm-instance treats resources as all-or-nothing, and cpu is per socket.
		{Name: "sized", Expression: `has(values.resources.cpu) && has(values.resources.sockets) && has(values.resources.memory)`},
		{Name: "it", Expression: `instanceType(values.instanceType)`},
	},
	Resources: map[string]string{
		"cpu":                    `sized ? int(values.resources.cpu) * int(values.resources.sockets) : it.cpu`,
		"dedicated-cpu":          `sized ? 0 : it['dedicated-cpu']`,
		"memory":                 `sized ? quantity(values.resources.memory) : it.memory`,
		"hugepages-2Mi":          `sized ? 0 : it['hugepages-2Mi']`,
		"hugepages-1Gi":          `sized ? 0 : it['hugepages-1Gi']`,
		"overcommitted-memory":   `sized ? 0 : it['overcommitted-memory']`,
		"services.loadbalancers": `values.external ? 1 : 0`,
	},
}

var kubernetesNodesDecl = &Declaration{
	Let: []Binding{{Name: "it", Expression: `instanceType(values.instanceType)`}},
	Resources: map[string]string{
		"cpu":    `it.cpu.mul(values.maxReplicas)`,
		"memory": `it.memory.mul(values.maxReplicas)`,
	},
}

func shippedSchema(t *testing.T, kind string) *structuralschema.Structural {
	t.Helper()
	raw, err := os.ReadFile("../../packages/system/" + kind + "-rd/cozyrds/" + kind + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	var def struct {
		Spec struct {
			Application struct {
				OpenAPISchema string `json:"openAPISchema"`
			} `json:"application"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &def); err != nil {
		t.Fatal(err)
	}
	s, err := defaulting.BuildSchema(def.Spec.Application.OpenAPISchema)
	if err != nil || s == nil {
		t.Fatalf("%s schema: %v", kind, err)
	}
	return s
}

func testEnvironment(t *testing.T) Environment {
	t.Helper()
	env, err := NewStaticEnvironment(shippedInstancetypes(t), InstancetypeOptions{}, map[string]ResourceList{
		"t1.nano":  {"cpu": resource.MustParse("250m"), "memory": resource.MustParse("128Mi"), "ephemeral-storage": resource.MustParse("2Gi")},
		"t1.micro": {"cpu": resource.MustParse("500m"), "memory": resource.MustParse("256Mi"), "ephemeral-storage": resource.MustParse("2Gi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func mustCompile(t *testing.T, decl *Declaration, kind string) *Evaluator {
	t.Helper()
	ev, err := Compile(decl, shippedSchema(t, kind))
	if err != nil {
		t.Fatalf("compile %s: %v", kind, err)
	}
	return ev
}

func rl(kv ...string) ResourceList {
	out := ResourceList{}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i]] = resource.MustParse(kv[i+1])
	}
	return out
}

func TestEvaluateKinds(t *testing.T) {
	env := testEnvironment(t)
	postgres := mustCompile(t, postgresDecl, "postgres")
	vm := mustCompile(t, vmInstanceDecl, "vm-instance")

	cases := []struct {
		name   string
		ev     *Evaluator
		values string
		want   ResourceList
	}{
		{
			// Two replicas of t1.micro with 10Gi each, all from schema defaults.
			name: "postgres defaults", ev: postgres, values: `{}`,
			want: rl("cpu", "1", "memory", "512Mi", "storage", "20Gi"),
		},
		{
			// The per-key fill: cpu from resources, memory from the preset.
			name: "postgres partial resources on a preset", ev: postgres,
			values: `{"replicas": 1, "resourcesPreset": "t1.nano", "resources": {"cpu": "1"}}`,
			want:   rl("cpu", "1", "memory", "128Mi", "storage", "10Gi"),
		},
		{
			name: "postgres integer cpu", ev: postgres,
			values: `{"replicas": 1, "resources": {"cpu": 2, "memory": "1Gi"}}`,
			want:   rl("cpu", "2", "memory", "1Gi", "storage", "10Gi"),
		},
		{
			name: "postgres autoscaling reserves the ceiling", ev: postgres,
			values: `{"autoscaling": {"enabled": true}}`,
			want:   rl("cpu", "3", "memory", "1536Mi", "storage", "60Gi"),
		},
		{
			name: "postgres autoscaling off reserves replicas", ev: postgres,
			values: `{"autoscaling": {"enabled": false, "maxReplicas": 6}}`,
			want:   rl("cpu", "1", "memory", "512Mi", "storage", "20Gi"),
		},
		{
			name: "vm-instance default instance type", ev: vm, values: `{}`,
			want: rl("cpu", "1", "memory", "4Gi"),
		},
		{
			name: "vm-instance dedicated type pins the emulator core", ev: vm,
			values: `{"instanceType": "cx1.2xlarge"}`,
			want:   rl("dedicated-cpu", "9", "hugepages-2Mi", "16Gi"),
		},
		{
			name: "vm-instance overcommitted type", ev: vm,
			values: `{"instanceType": "o1.large"}`,
			want:   rl("cpu", "2", "overcommitted-memory", "8Gi"),
		},
		{
			// cpu is cores per socket.
			name: "vm-instance complete resources block", ev: vm,
			values: `{"instanceType": "u1.small", "resources": {"cpu": 4, "sockets": 2, "memory": "8Gi"}}`,
			want:   rl("cpu", "8", "memory", "8Gi"),
		},
		{
			// Without sockets the chart sizes the VM from its instance type.
			name: "vm-instance cpu without sockets", ev: vm,
			values: `{"instanceType": "u1.small", "resources": {"cpu": 4, "memory": "8Gi"}}`,
			want:   rl("cpu", "1", "memory", "2Gi"),
		},
		{
			name: "vm-instance external", ev: vm,
			values: `{"instanceType": "u1.small", "external": true}`,
			want:   rl("cpu", "1", "memory", "2Gi", "services.loadbalancers", "1"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.ev.EvaluateJSON([]byte(tc.values), env)
			if err != nil {
				t.Fatal(err)
			}
			assertList(t, tc.name, got, tc.want)
		})
	}
}

// A release a platform chart renders, such as ComputePlane's default md0
// pool, carries only the keys the chart wrote. It has to reserve exactly what
// the same pool with its defaults written out reserves.
func TestEvaluateDefaultsStoredValues(t *testing.T) {
	env := testEnvironment(t)
	ev := mustCompile(t, kubernetesNodesDecl, "kubernetes-nodes")

	sparse, err := ev.EvaluateJSON([]byte(`{"roles": ["ingress-nginx"], "minReplicas": 0}`), env)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := ev.EvaluateJSON([]byte(`{"roles": ["ingress-nginx"], "minReplicas": 0, "maxReplicas": 10, "instanceType": "u1.medium"}`), env)
	if err != nil {
		t.Fatal(err)
	}
	assertList(t, "sparse vs explicit", sparse, explicit)
	assertList(t, "sparse", sparse, rl("cpu", "10", "memory", "40Gi"))
}

func TestEvaluateFailsClosed(t *testing.T) {
	env := testEnvironment(t)
	postgres := mustCompile(t, postgresDecl, "postgres")
	vm := mustCompile(t, vmInstanceDecl, "vm-instance")

	if _, err := vm.EvaluateJSON([]byte(`{"instanceType": "u9.imaginary"}`), env); err == nil {
		t.Error("unknown instance type: expected an error")
	} else if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown instance type: expected ErrNotFound, got %v", err)
	}
	if _, err := postgres.EvaluateJSON([]byte(`{"resourcesPreset": "t9.huge"}`), env); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown preset: expected ErrNotFound, got %v", err)
	}
	if _, err := postgres.EvaluateJSON([]byte(`{"replicas": "two"}`), env); err == nil {
		t.Error("wrong value type: expected an error")
	}
	if _, err := postgres.EvaluateJSON([]byte(`{"size": "lots"}`), env); err == nil {
		t.Error("unparseable quantity: expected an error")
	}
	if _, err := postgres.EvaluateJSON([]byte(`[1, 2]`), env); err == nil {
		t.Error("non-object values: expected an error")
	}
	if _, err := postgres.EvaluateJSON([]byte(`{}`), nil); err == nil {
		t.Error("no environment: expected an error")
	}

	unguarded, err := Compile(&Declaration{Resources: map[string]string{"cpu": `quantity(values.notInTheSchema)`}}, shippedSchema(t, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unguarded.EvaluateJSON([]byte(`{}`), env); err == nil {
		t.Error("unguarded absent key: expected an error, not a zero")
	}

	negative, err := Compile(&Declaration{Resources: map[string]string{"cpu": `-1`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := negative.EvaluateJSON(nil, env); err == nil {
		t.Error("negative reservation: expected an error")
	}
}

func TestCompile(t *testing.T) {
	if _, err := Compile(nil, nil); !errors.Is(err, ErrNoDeclaration) {
		t.Errorf("nil declaration: got %v, want ErrNoDeclaration", err)
	}
	if _, err := Compile(&Declaration{}, nil); !errors.Is(err, ErrNoDeclaration) {
		t.Errorf("empty declaration: got %v, want ErrNoDeclaration", err)
	}

	exempt, err := Compile(&Declaration{Exempt: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := exempt.EvaluateJSON([]byte(`{"anything": 1}`), nil)
	if err != nil || len(got) != 0 || !exempt.Exempt() {
		t.Errorf("exempt: got %v, %v", got, err)
	}

	invalid := map[string]*Declaration{
		"exempt with resources": {Exempt: true, Resources: map[string]string{"cpu": "1"}},
		"syntax error":          {Resources: map[string]string{"cpu": "values.("}},
		"undeclared variable":   {Resources: map[string]string{"cpu": "count"}},
		"empty key":             {Resources: map[string]string{"": "1"}},
		"bad let name":          {Let: []Binding{{Name: "a-b", Expression: "1"}}, Resources: map[string]string{"cpu": "1"}},
		"let shadows values":    {Let: []Binding{{Name: "values", Expression: "1"}}, Resources: map[string]string{"cpu": "1"}},
		"duplicate let":         {Let: []Binding{{Name: "a", Expression: "1"}, {Name: "a", Expression: "2"}}, Resources: map[string]string{"cpu": "a"}},
	}
	for name, decl := range invalid {
		if _, err := Compile(decl, nil); err == nil {
			t.Errorf("%s: expected a compile error", name)
		}
	}
}

func TestEvaluateIsConcurrencySafe(t *testing.T) {
	env := testEnvironment(t)
	vm := mustCompile(t, vmInstanceDecl, "vm-instance")
	var wg sync.WaitGroup
	for _, it := range []string{"u1.small", "cx1.2xlarge", "o1.large", "u1.medium"} {
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				want, err := ClassifyInstancetype(findInstancetype(t, it), InstancetypeOptions{})
				if err != nil {
					t.Error(err)
					return
				}
				got, err := vm.EvaluateJSON([]byte(`{"instanceType": "`+it+`"}`), env)
				if err != nil {
					t.Error(err)
					return
				}
				assertList(t, it, got, want)
			}()
		}
	}
	wg.Wait()
}

func findInstancetype(t *testing.T, name string) map[string]any {
	for _, obj := range shippedInstancetypes(t) {
		if obj["metadata"].(map[string]any)["name"] == name {
			return obj
		}
	}
	t.Fatalf("%s is not shipped", name)
	return nil
}

func TestAggregate(t *testing.T) {
	env := testEnvironment(t)
	evaluators := map[string]*Evaluator{
		"VMInstance": mustCompile(t, vmInstanceDecl, "vm-instance"),
		"Postgres":   mustCompile(t, postgresDecl, "postgres"),
	}
	lookup := func(kind, group string) (*Evaluator, error) {
		if group != "apps.cozystack.io" {
			return nil, errors.New("unknown group")
		}
		if ev, ok := evaluators[kind]; ok {
			return ev, nil
		}
		return nil, ErrNoDeclaration
	}
	apps := []Application{
		{Namespace: "tenant-a", Name: "vm1", Kind: "VMInstance", Group: "apps.cozystack.io", Values: []byte(`{"instanceType": "u1.small"}`)},
		{Namespace: "tenant-a", Name: "vm2", Kind: "VMInstance", Group: "apps.cozystack.io", Values: []byte(`{"instanceType": "u1.small", "external": true}`)},
		{Namespace: "tenant-a", Name: "db", Kind: "Postgres", Group: "apps.cozystack.io", Values: []byte(`{}`)},
		{Namespace: "tenant-b", Name: "vm3", Kind: "VMInstance", Group: "apps.cozystack.io", Values: []byte(`{"instanceType": "u1.small"}`)},
		{Namespace: "tenant-b", Name: "broken", Kind: "VMInstance", Group: "apps.cozystack.io", Values: []byte(`{"instanceType": "gone"}`)},
		{Namespace: "tenant-b", Name: "undeclared", Kind: "Redis", Group: "apps.cozystack.io", Values: []byte(`{}`)},
	}
	sums, failures := Aggregate(apps, lookup, env)
	assertList(t, "tenant-a", sums["tenant-a"], rl("cpu", "3", "memory", "4608Mi", "storage", "20Gi", "services.loadbalancers", "1"))
	assertList(t, "tenant-b", sums["tenant-b"], rl("cpu", "1", "memory", "2Gi"))
	if len(failures) != 2 {
		t.Fatalf("expected 2 failures, got %v", failures)
	}
	if failures[0].Application.Name != "broken" || !errors.Is(failures[0].Err, ErrNotFound) {
		t.Errorf("first failure: %v", failures[0])
	}
	if failures[1].Application.Name != "undeclared" || !errors.Is(failures[1].Err, ErrNoDeclaration) {
		t.Errorf("second failure: %v", failures[1])
	}
}

func TestResourceListArithmetic(t *testing.T) {
	a := rl("cpu", "2", "memory", "4Gi")
	b := a.DeepCopy()
	b.Add(rl("cpu", "1", "storage", "10Gi"))
	assertList(t, "copy unaffected", a, rl("cpu", "2", "memory", "4Gi"))
	assertList(t, "add", b, rl("cpu", "3", "memory", "4Gi", "storage", "10Gi"))
	b.Sub(rl("cpu", "4", "services.loadbalancers", "1"))
	assertList(t, "sub", b, rl("cpu", "-1", "memory", "4Gi", "storage", "10Gi", "services.loadbalancers", "-1"))
	if keys := b.Keys(); len(keys) != 4 || keys[0] != "cpu" || keys[3] != "storage" {
		t.Errorf("keys not sorted: %v", keys)
	}
}
