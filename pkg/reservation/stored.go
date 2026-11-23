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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The annotations below live on HelmReleases, outside the
// apps.cozystack.io- prefix through which an Application's own annotations
// reach its release, so a tenant can neither write them nor read them back
// as metadata.
const (
	// ReservedAnnotation carries, on an application's HelmRelease, the
	// figure it reserves: written by the apiserver gate when it admits the
	// write, and by the tenant-quota controller for a release the gate never
	// admitted.
	ReservedAnnotation = "reservation.cozystack.io/reserved"
	// PoolAnnotation carries, on the HelmRelease of a Tenant that roots a
	// pool, the pool's figures as the tenant-quota controller last observed
	// them. The apiserver projects it into the Tenant's status.
	PoolAnnotation = "reservation.cozystack.io/pool"
)

// MaxAnnotationSize bounds the value of ReservedAnnotation and
// PoolAnnotation, well inside the 256KiB the API server allows for all of an
// object's annotations together. The gate refuses a reservation larger than
// this, and the controller writes nothing larger either.
const MaxAnnotationSize = 16 << 10

// Pool condition types.
const (
	// ConditionOvercommitted is True when what a pool's applications
	// reserve plus what its sub-tenants carved out exceeds its budget.
	ConditionOvercommitted = "Overcommitted"
	// ConditionReservationIncomplete is True when an application of the
	// pool does not evaluate, so the pool's figure counts it at its last
	// stored reservation or, with none, not at all.
	ConditionReservationIncomplete = "ReservationIncomplete"
)

// Stored is the value of ReservedAnnotation. ValuesHash ties the figure to
// the values it was computed from, so that a figure written before an edit
// made with the gate off is recognised as stale instead of trusted.
type Stored struct {
	ValuesHash string       `json:"valuesHash"`
	Resources  ResourceList `json:"resources"`
}

// ValuesHash hashes values in a canonical form: the apiserver may hand back
// a stored blob with its keys in another order than the one written. Numbers
// keep their literal, since a float64 would hash two integers above 2^53
// alike.
func ValuesHash(raw []byte) (string, error) {
	var v any
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return "", err
		}
		// More reports false before a stray ] or }, so only io.EOF proves
		// nothing follows the document; trailing whitespace is still fine.
		if _, err := dec.Token(); err != io.EOF {
			return "", fmt.Errorf("values carry data after their JSON document")
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

// PoolStatus is the value of PoolAnnotation.
type PoolStatus struct {
	// Reserved is what the applications in the pool's namespaces reserve.
	Reserved ResourceList `json:"reserved,omitempty"`
	// CarvedOut is what the pool's bounded sub-tenants declared, each of
	// which is a slice of this pool's budget.
	CarvedOut ResourceList `json:"carvedOut,omitempty"`
	// Budget is the pool root's declared quota.
	Budget ResourceList `json:"budget,omitempty"`
	// Conditions are ConditionOvercommitted and
	// ConditionReservationIncomplete.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ReadPoolStatus reads PoolAnnotation from annotations. It returns nil and
// no error when the annotation is absent.
func ReadPoolStatus(annotations map[string]string) (*PoolStatus, error) {
	raw, ok := annotations[PoolAnnotation]
	if !ok {
		return nil, nil
	}
	var s PoolStatus
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("%s: %w", PoolAnnotation, err)
	}
	return &s, nil
}

// Encode renders s as the value of PoolAnnotation.
func (s *PoolStatus) Encode() (string, error) {
	raw, err := json.Marshal(s)
	return string(raw), err
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
// An error wrapping ErrUnavailable is returned whatever was stored.
func Resolve(ev *Evaluator, values []byte, prev *Stored, env Environment) (ResourceList, Source, error) {
	if prev.Matches(values) {
		return prev.Resources, FromStored, nil
	}
	rl, err := ev.EvaluateJSON(values, env)
	if err == nil {
		return rl, Evaluated, nil
	}
	// The environment could not be read: the values may evaluate fine, so
	// this is neither a stale figure nor a broken declaration, and the
	// caller has to retry rather than sum without it.
	if errors.Is(err, ErrUnavailable) {
		return nil, Evaluated, err
	}
	if prev != nil {
		return prev.Resources, StaleStored, nil
	}
	return nil, Evaluated, err
}
