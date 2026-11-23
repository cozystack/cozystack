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

package application

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cozystack/cozystack/pkg/reservation"
)

// The tenant-quota controller reads and writes the figure the gate stores,
// through pkg/reservation. These tests fail if the two sides stop agreeing
// on the annotation, its JSON shape, the values hash or the budget keys:
// the controller would then distrust every figure the gate wrote, or the
// gate every figure the controller wrote.

func TestControllerSharesTheGatesReservedAnnotation(t *testing.T) {
	if ReservedAnnotation != reservation.ReservedAnnotation {
		t.Fatalf("gate writes %q, controller reads %q", ReservedAnnotation, reservation.ReservedAnnotation)
	}
	written := storedReservation{ValuesHash: "h", Resources: reservation.ResourceList{"cpu": resource.MustParse("2")}}
	raw, err := json.Marshal(written)
	if err != nil {
		t.Fatal(err)
	}
	read, err := reservation.ReadStored(map[string]string{reservation.ReservedAnnotation: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if q := read.Resources["cpu"]; read.ValuesHash != "h" || q.Cmp(resource.MustParse("2")) != 0 {
		t.Fatalf("the controller reads the gate's figure as %+v", read)
	}
}

func TestControllerHashesValuesLikeTheGate(t *testing.T) {
	for _, values := range []string{
		``,
		`{}`,
		`{"instanceType":"u1.small"}`,
		`{"b":{"d":3,"c":2},"a":1}`,
		`{"resources":{"cpu":2,"memory":"4Gi"},"disks":[{"name":"root"}]}`,
	} {
		gate, err := valuesHash([]byte(values))
		if err != nil {
			t.Fatal(err)
		}
		controller, err := reservation.ValuesHash([]byte(values))
		if err != nil {
			t.Fatal(err)
		}
		if gate != controller {
			t.Errorf("values %s: the gate hashes %s, the controller %s", values, gate, controller)
		}
	}
}

func TestControllerMapsBudgetKeysLikeTheGate(t *testing.T) {
	for _, k := range []string{"cpu", "requests.cpu", "limits.memory", "requests.storage", "services.loadbalancers"} {
		if got, want := reservation.BudgetKey(k), budgetKey(k); got != want {
			t.Errorf("budget key %q: the gate maps it to %q, the controller to %q", k, want, got)
		}
	}
}
