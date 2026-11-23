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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// ReservedAnnotation carries, on an application's HelmRelease, the figure
// it reserves. It lives outside the apps.cozystack.io- prefix through which
// an Application's own annotations reach its release, so a tenant can
// neither write it nor read it back as metadata.
const ReservedAnnotation = "reservation.cozystack.io/reserved"

// Stored is the value of ReservedAnnotation. ValuesHash ties the figure to
// the values it was computed from, so that a figure written before an edit
// made with the gate off is recognised as stale instead of trusted.
type Stored struct {
	ValuesHash string       `json:"valuesHash"`
	Resources  ResourceList `json:"resources"`
}

// ValuesHash hashes values in a canonical form: the apiserver may hand back
// a stored blob with its keys in another order than the one written.
func ValuesHash(raw []byte) (string, error) {
	var v any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", err
		}
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// ReadStored reads ReservedAnnotation from annotations. It returns nil and
// no error when the annotation is absent.
func ReadStored(annotations map[string]string) (*Stored, error) {
	raw, ok := annotations[ReservedAnnotation]
	if !ok {
		return nil, nil
	}
	var s Stored
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("%s: %w", ReservedAnnotation, err)
	}
	return &s, nil
}

// Matches reports whether s was computed from values. Safe on a nil s.
func (s *Stored) Matches(values []byte) bool {
	if s == nil {
		return false
	}
	hash, err := ValuesHash(values)
	return err == nil && hash == s.ValuesHash
}

// Encode renders s as the value of ReservedAnnotation.
func (s *Stored) Encode() (string, error) {
	raw, err := json.Marshal(s)
	return string(raw), err
}

// BudgetKey maps a resourceQuotas key onto the reservation vocabulary. An
// operator may write requests.storage where a declaration reserves storage;
// both name the same budget.
func BudgetKey(k string) string {
	if rest, ok := strings.CutPrefix(k, "requests."); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(k, "limits."); ok {
		return rest
	}
	return k
}

// Budget maps a tenant's declared resourceQuotas onto reservation keys. Two
// declared keys that name the same budget keep the tighter figure.
func Budget(declared map[string]resource.Quantity) ResourceList {
	out := ResourceList{}
	for k, v := range declared {
		key := BudgetKey(k)
		if cur, ok := out[key]; ok && cur.Cmp(v) <= 0 {
			continue
		}
		out[key] = v.DeepCopy()
	}
	return out
}

// TenantBudget reads a tenant's budget from its values, in reservation keys.
// A tenant that declares no resourceQuotas has an empty budget.
func TenantBudget(values []byte) (ResourceList, error) {
	if len(values) == 0 {
		return ResourceList{}, nil
	}
	var v struct {
		ResourceQuotas map[string]resource.Quantity `json:"resourceQuotas"`
	}
	if err := json.Unmarshal(values, &v); err != nil {
		return nil, fmt.Errorf("decode tenant values: %w", err)
	}
	return Budget(v.ResourceQuotas), nil
}

// Source says where Resolve took an application's figure from.
type Source int

const (
	// FromStored is the stored figure, which still matches the values.
	FromStored Source = iota
	// Evaluated is a fresh evaluation of the values: there was no stored
	// figure, or it was computed from other values.
	Evaluated
	// StaleStored is a stored figure computed from other values, kept
	// because the values no longer evaluate.
	StaleStored
)

// Resolve returns what an application reserves.
//
// The stored figure is used while it still matches the values. An
// application that no longer evaluates keeps the last figure stored for it,
// reported as StaleStored, rather than dropping to zero and handing its share
// of the pool to the next writer. With no stored figure either, the
// evaluation error is returned and the application is left out of the sum.
func Resolve(ev *Evaluator, values []byte, prev *Stored, env Environment) (ResourceList, Source, error) {
	if prev.Matches(values) {
		return prev.Resources, FromStored, nil
	}
	rl, err := ev.EvaluateJSON(values, env)
	if err == nil {
		return rl, Evaluated, nil
	}
	if prev != nil {
		return prev.Resources, StaleStored, nil
	}
	return nil, Evaluated, err
}
