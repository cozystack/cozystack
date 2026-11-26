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
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestValuesHash_IgnoresKeyOrder(t *testing.T) {
	a, err := ValuesHash([]byte(`{"a":1,"b":{"c":2,"d":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ValuesHash([]byte(`{"b":{"d":3,"c":2},"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("the same values hash differently by key order: %s != %s", a, b)
	}
	c, _ := ValuesHash([]byte(`{"a":2}`))
	if a == c {
		t.Error("different values hash the same")
	}
	big1, err := ValuesHash([]byte(`{"n":9007199254740992}`))
	if err != nil {
		t.Fatal(err)
	}
	big2, err := ValuesHash([]byte(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if big1 == big2 {
		t.Error("two integers above 2^53 hash the same")
	}
	for _, trailing := range []string{`{"n":1}]`, `{"n":1}}`, `{"n":1} {"n":2}`, `{"n":1} x`} {
		if _, err := ValuesHash([]byte(trailing)); err == nil {
			t.Errorf("values %s with data after the document hashed", trailing)
		}
	}
	if _, err := ValuesHash([]byte("{\"n\":1}\n  ")); err != nil {
		t.Errorf("trailing whitespace refused: %v", err)
	}
}

func TestTenantBudget_MapsQuotaKeysOntoReservationKeys(t *testing.T) {
	got, err := TenantBudget([]byte(`{"resourceQuotas":{"cpu":"10","requests.storage":"100Gi","limits.memory":"8Gi","memory":"16Gi"},"host":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"cpu": "10", "storage": "100Gi", "memory": "8Gi"}
	if len(got) != len(want) {
		t.Fatalf("budget = %v, want %v", got, want)
	}
	for k, v := range want {
		if q := got[k]; q.Cmp(resource.MustParse(v)) != 0 {
			t.Errorf("budget[%s] = %s, want %s (the tighter of two keys naming it)", k, q.String(), v)
		}
	}

	if got, err := TenantBudget(nil); err != nil || len(got) != 0 {
		t.Errorf("TenantBudget(nil) = %v, %v; want an empty budget", got, err)
	}
	if _, err := TenantBudget([]byte(`{"resourceQuotas":{"cpu":"lots"}}`)); err == nil {
		t.Error("an unparseable quota was accepted")
	}
}

func TestResolve(t *testing.T) {
	ev, err := Compile(&Declaration{Resources: map[string]string{"cpu": "quantity(values.cpu)"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := &StaticEnvironment{}
	values := []byte(`{"cpu":"2"}`)
	hash, _ := ValuesHash(values)
	stored := func(h, cpu string) *Stored {
		return &Stored{ValuesHash: h, Resources: ResourceList{"cpu": resource.MustParse(cpu)}}
	}

	for _, tc := range []struct {
		name    string
		values  []byte
		prev    *Stored
		wantCPU string
		wantSrc Source
		wantErr bool
	}{
		{name: "a matching figure is trusted as stored", values: values, prev: stored(hash, "7"), wantCPU: "7", wantSrc: FromStored},
		{name: "a figure from other values is re-evaluated", values: values, prev: stored("other", "7"), wantCPU: "2", wantSrc: Evaluated},
		{name: "no figure is evaluated", values: values, wantCPU: "2", wantSrc: Evaluated},
		{name: "values that no longer evaluate keep the last figure", values: []byte(`{"cpu":"lots"}`), prev: stored("other", "7"), wantCPU: "7", wantSrc: StaleStored},
		{name: "values that do not evaluate with no figure fail", values: []byte(`{"cpu":"lots"}`), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rl, src, err := Resolve(ev, tc.values, tc.prev, env)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %v, want an error", rl)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if src != tc.wantSrc {
				t.Errorf("source = %d, want %d", src, tc.wantSrc)
			}
			if q := rl["cpu"]; q.Cmp(resource.MustParse(tc.wantCPU)) != 0 {
				t.Errorf("cpu = %s, want %s", q.String(), tc.wantCPU)
			}
		})
	}
}

func TestPoolStatus_RoundTrips(t *testing.T) {
	in := &PoolStatus{
		Reserved: ResourceList{"cpu": resource.MustParse("1500m")},
		Budget:   ResourceList{"cpu": resource.MustParse("4")},
	}
	raw, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := ReadPoolStatus(map[string]string{PoolAnnotation: raw})
	if err != nil {
		t.Fatal(err)
	}
	if q := out.Reserved["cpu"]; q.Cmp(resource.MustParse("1.5")) != 0 {
		t.Errorf("reserved cpu = %s after a round trip", q.String())
	}
	again, _ := out.Encode()
	if again != raw {
		t.Errorf("re-encoding is not stable: %s != %s", again, raw)
	}
	if s, err := ReadPoolStatus(nil); s != nil || err != nil {
		t.Errorf("ReadPoolStatus(nil) = %v, %v; want nil, nil", s, err)
	}
}

// unavailableEnv fails every lookup the way ClusterEnvironment does when
// it cannot list instance types.
type unavailableEnv struct{}

func (unavailableEnv) Instancetype(string) (ResourceList, error) {
	return nil, fmt.Errorf("list instance types: %w", ErrUnavailable)
}

func (unavailableEnv) Preset(string) (ResourceList, error) {
	return nil, fmt.Errorf("presets: %w", ErrUnavailable)
}

func TestResolve_UnavailableIsNeverAStaleFigure(t *testing.T) {
	ev, err := Compile(&Declaration{Resources: map[string]string{"cpu": "instanceType(values.t).cpu"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	prev := &Stored{ValuesHash: "other values", Resources: ResourceList{"cpu": resource.MustParse("7")}}
	if _, _, err := Resolve(ev, []byte(`{"t":"u1.small"}`), prev, unavailableEnv{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Resolve = %v, want the ErrUnavailable error rather than the stale figure", err)
	}
}
