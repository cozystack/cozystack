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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/functions"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/api/resource"
	apiservercel "k8s.io/apiserver/pkg/cel"
	"k8s.io/apiserver/pkg/cel/library"

	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
)

// ErrNoDeclaration is returned for a kind whose definition declares neither
// its consumption nor an exemption. Such a kind is refused rather than
// treated as free: for an out-of-tree catalog an omitted declaration would
// otherwise be a quota-escalation vector no in-tree test can reach.
var ErrNoDeclaration = errors.New("the application definition declares neither its consumption nor an exemption")

const (
	valuesVar        = "values"
	instancetypeFunc = "instanceType"
	presetFunc       = "preset"

	// costLimit bounds one expression. Declarations are platform-authored,
	// but they run on the admission path, so a runaway comprehension must
	// fail rather than stall a write.
	costLimit = 1_000_000
)

var bindingName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Evaluator computes the reservation of one kind's applications. It is built
// once per declaration and is safe for concurrent use.
type Evaluator struct {
	exempt    bool
	schema    *structuralschema.Structural
	env       *cel.Env
	lets      []compiled
	resources []compiled
}

type compiled struct {
	name string
	ast  *cel.Ast
}

// Compile checks decl and prepares it for evaluation. schema is the kind's
// structural OpenAPI schema; stored values are defaulted against it before
// any expression reads them, because the aggregated apiserver persists a
// tenant's spec without its defaults. A nil decl yields ErrNoDeclaration.
func Compile(decl *Declaration, schema *structuralschema.Structural) (*Evaluator, error) {
	if decl == nil {
		return nil, ErrNoDeclaration
	}
	if decl.Exempt {
		if len(decl.Let) > 0 || len(decl.Resources) > 0 {
			return nil, fmt.Errorf("an exempt declaration must not carry let or resources")
		}
		return &Evaluator{exempt: true}, nil
	}
	if len(decl.Resources) == 0 {
		return nil, ErrNoDeclaration
	}

	opts := []cel.EnvOption{
		cel.Variable(valuesVar, cel.DynType),
		library.Quantity(),
		cel.Function(instancetypeFunc,
			cel.Overload("instanceType_string", []*cel.Type{cel.StringType}, cel.MapType(cel.StringType, cel.DynType),
				cel.LateFunctionBinding())),
		cel.Function(presetFunc,
			cel.Overload("preset_string", []*cel.Type{cel.StringType}, cel.MapType(cel.StringType, cel.DynType),
				cel.LateFunctionBinding())),
		cel.Function("quantity",
			cel.Overload("quantity_int", []*cel.Type{cel.IntType}, apiservercel.QuantityType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					return apiservercel.Quantity{Quantity: resource.NewQuantity(int64(v.(types.Int)), resource.DecimalSI)}
				}))),
		// The standard _*_ is a singleton dispatching on the left operand's
		// traits, so it cannot take a quantity overload; scaling is a method.
		cel.Function("mul",
			cel.MemberOverload("quantity_mul_int", []*cel.Type{apiservercel.QuantityType, cel.IntType}, apiservercel.QuantityType,
				cel.BinaryBinding(mulQuantity))),
	}
	seen := map[string]bool{valuesVar: true}
	for _, b := range decl.Let {
		if !bindingName.MatchString(b.Name) {
			return nil, fmt.Errorf("let %q: not a valid identifier", b.Name)
		}
		if seen[b.Name] {
			return nil, fmt.Errorf("let %q: name already bound", b.Name)
		}
		seen[b.Name] = true
		opts = append(opts, cel.Variable(b.Name, cel.DynType))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("build CEL environment: %w", err)
	}

	ev := &Evaluator{schema: schema, env: env}
	for _, b := range decl.Let {
		ast, iss := env.Compile(b.Expression)
		if iss.Err() != nil {
			return nil, fmt.Errorf("let %q: %w", b.Name, iss.Err())
		}
		ev.lets = append(ev.lets, compiled{name: b.Name, ast: ast})
	}
	for _, key := range slices.Sorted(maps.Keys(decl.Resources)) {
		if key == "" {
			return nil, fmt.Errorf("resources: empty quota key")
		}
		ast, iss := env.Compile(decl.Resources[key])
		if iss.Err() != nil {
			return nil, fmt.Errorf("resources[%q]: %w", key, iss.Err())
		}
		ev.resources = append(ev.resources, compiled{name: key, ast: ast})
	}
	return ev, nil
}

// Exempt reports whether the kind declared that it consumes nothing.
func (e *Evaluator) Exempt() bool { return e.exempt }

// EvaluateJSON evaluates the reservation of an application from its values
// as stored, a JSON object.
func (e *Evaluator) EvaluateJSON(raw []byte, env Environment) (ResourceList, error) {
	values := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("decode values: %w", err)
		}
		m, ok := v.(map[string]any)
		if !ok && v != nil {
			return nil, fmt.Errorf("decode values: expected an object, got %T", v)
		}
		if m != nil {
			values = m
		}
	}
	return e.Evaluate(values, env)
}

// Evaluate returns the reservation of an application with the given values.
// The values are defaulted first and are not modified. Anything that cannot
// be resolved — an unknown instance type, a value of the wrong type, a key an
// expression reads without guarding it with has() — is an error, never a
// smaller figure. Keys that evaluate to zero are omitted.
func (e *Evaluator) Evaluate(values map[string]any, env Environment) (ResourceList, error) {
	if e.exempt {
		return ResourceList{}, nil
	}
	if env == nil {
		return nil, fmt.Errorf("no environment to resolve instance types and presets against")
	}

	defaulted := normalizeNumbers(values).(map[string]any)
	if err := defaulting.Apply(&defaulted, e.schema); err != nil {
		return nil, fmt.Errorf("default values: %w", err)
	}

	// The environment differs per call, so instanceType and preset are
	// declared with a late binding and bound here, per program; cel.Functions
	// is deprecated for declaring functions but is still how cel-go supplies
	// a late binding.
	bindings := cel.Functions(
		&functions.Overload{Operator: "instanceType_string", Unary: lookup(env.Instancetype, instancetypeZeroKeys)},
		&functions.Overload{Operator: "preset_string", Unary: lookup(env.Preset, nil)},
	)
	activation := map[string]any{valuesVar: defaulted}

	for _, c := range e.lets {
		out, err := e.run(c, bindings, activation)
		if err != nil {
			return nil, fmt.Errorf("let %q: %w", c.name, err)
		}
		activation[c.name] = out
	}

	result := ResourceList{}
	for _, c := range e.resources {
		out, err := e.run(c, bindings, activation)
		if err != nil {
			return nil, fmt.Errorf("resources[%q]: %w", c.name, err)
		}
		q, err := toQuantity(out)
		if err != nil {
			return nil, fmt.Errorf("resources[%q]: %w", c.name, err)
		}
		if q.Sign() < 0 {
			return nil, fmt.Errorf("resources[%q]: negative reservation %s", c.name, q.String())
		}
		if q.IsZero() {
			continue
		}
		result[c.name] = q
	}
	return result, nil
}

func (e *Evaluator) run(c compiled, bindings cel.ProgramOption, activation map[string]any) (ref.Val, error) {
	prg, err := e.env.Program(c.ast, bindings, cel.CostLimit(costLimit))
	if err != nil {
		return nil, err
	}
	out, _, err := prg.Eval(activation)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// instancetypeZeroKeys are present, at zero, in every instanceType() result,
// so that a declaration can read any class of a type without guarding it.
var instancetypeZeroKeys = []string{
	KeyCPU, KeyDedicatedCPU, KeyMemory, KeyOvercommittedMemory,
	KeyHugepagesPrefix + "2Mi", KeyHugepagesPrefix + "1Gi",
}

func lookup(resolve func(string) (ResourceList, error), zeroKeys []string) functions.UnaryOp {
	return func(arg ref.Val) ref.Val {
		name, ok := arg.(types.String)
		if !ok {
			return types.MaybeNoSuchOverloadErr(arg)
		}
		rl, err := resolve(string(name))
		if err != nil {
			return types.WrapErr(err)
		}
		out := make(map[string]any, len(rl)+len(zeroKeys))
		for _, k := range zeroKeys {
			out[k] = apiservercel.Quantity{Quantity: resource.NewQuantity(0, resource.DecimalSI)}
		}
		for k, v := range rl {
			q := v.DeepCopy()
			out[k] = apiservercel.Quantity{Quantity: &q}
		}
		return types.DefaultTypeAdapter.NativeToValue(out)
	}
}

func mulQuantity(q, n ref.Val) ref.Val {
	qq, ok := q.(apiservercel.Quantity)
	if !ok {
		return types.MaybeNoSuchOverloadErr(q)
	}
	nn, ok := n.(types.Int)
	if !ok {
		return types.MaybeNoSuchOverloadErr(n)
	}
	out := qq.Quantity.DeepCopy()
	if !out.Mul(int64(nn)) {
		return types.NewErr("quantity %s * %d overflows", qq.Quantity.String(), int64(nn))
	}
	return apiservercel.Quantity{Quantity: &out}
}

func toQuantity(v ref.Val) (resource.Quantity, error) {
	switch t := v.(type) {
	case apiservercel.Quantity:
		return t.Quantity.DeepCopy(), nil
	case types.Int:
		return *resource.NewQuantity(int64(t), resource.DecimalSI), nil
	case types.Uint:
		if uint64(t) > math.MaxInt64 {
			return resource.Quantity{}, fmt.Errorf("%d overflows a quantity", uint64(t))
		}
		return *resource.NewQuantity(int64(t), resource.DecimalSI), nil
	case types.Double:
		f := float64(t)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return resource.Quantity{}, fmt.Errorf("not a finite number: %v", f)
		}
		return *resource.NewMilliQuantity(int64(math.Ceil(f*1000)), resource.DecimalSI), nil
	case types.String:
		q, err := resource.ParseQuantity(string(t))
		if err != nil {
			return resource.Quantity{}, fmt.Errorf("parse %q as a quantity: %w", string(t), err)
		}
		return q, nil
	default:
		return resource.Quantity{}, fmt.Errorf("expression returned %s, expected an int, a double, a string or a quantity", v.Type().TypeName())
	}
}

// normalizeNumbers returns a copy of v in which every JSON number is an int64
// when it is integral and a float64 otherwise. Without it every number in a
// stored spec reaches CEL as a double, and replicas * quantity(...) would not
// type-check at runtime.
func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = normalizeNumbers(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = normalizeNumbers(vv)
		}
		return out
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t)
		}
		return t
	case int:
		return int64(t)
	default:
		return v
	}
}
