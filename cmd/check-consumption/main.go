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

// Command check-consumption reads rendered manifests on stdin and checks that
// every consumption declaration on an ApplicationDefinition compiles against
// its kind's schema and evaluates on the kind's defaults, resolving instance
// types against the VirtualMachineClusterInstancetypes in the same stream.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
	"github.com/cozystack/cozystack/pkg/reservation"
)

// errPresetsNotRendered ends an evaluation at a preset lookup: cozy-lib's
// preset table is a template helper, not an object a chart renders.
var errPresetsNotRendered = errors.New("presets are not rendered")

type renderedEnvironment struct{ *reservation.StaticEnvironment }

func (renderedEnvironment) Preset(name string) (reservation.ResourceList, error) {
	return nil, fmt.Errorf("preset %q: %w", name, errPresetsNotRendered)
}

func check(in io.Reader, out io.Writer) error {
	var defs []*cozyv1alpha1.ApplicationDefinition
	var instancetypes []map[string]any
	dec := utilyaml.NewYAMLOrJSONDecoder(in, 4096)
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		switch obj["kind"] {
		case "VirtualMachineClusterInstancetype":
			instancetypes = append(instancetypes, obj)
		case "ApplicationDefinition":
			raw, err := json.Marshal(obj)
			if err != nil {
				return err
			}
			def := &cozyv1alpha1.ApplicationDefinition{}
			if err := json.Unmarshal(raw, def); err != nil {
				return err
			}
			defs = append(defs, def)
		}
	}
	static, err := reservation.NewStaticEnvironment(instancetypes, nil)
	if err != nil {
		return err
	}
	env := renderedEnvironment{static}

	var errs []error
	for _, def := range defs {
		if def.Spec.Consumption == nil {
			continue
		}
		schema, err := defaulting.BuildSchema(def.Spec.Application.OpenAPISchema)
		if err == nil {
			var ev *reservation.Evaluator
			if ev, err = reservation.Compile(def.Spec.Consumption, schema); err == nil {
				_, err = ev.EvaluateJSON([]byte(`{}`), env)
			}
		}
		switch {
		case errors.Is(err, errPresetsNotRendered):
			fmt.Fprintf(out, "%s: compiled; evaluation on defaults stopped at a preset lookup\n", def.Name)
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", def.Name, err))
		default:
			fmt.Fprintf(out, "%s: ok\n", def.Name)
		}
	}
	return errors.Join(errs...)
}

func main() {
	if err := check(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
