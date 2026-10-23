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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrNotFound is wrapped by an Environment when a named instance type or
// preset does not exist. The evaluator fails closed on it rather than
// reserving nothing.
var ErrNotFound = errors.New("not found")

// Environment resolves the cluster objects a declaration may reference by
// name. Implementations must be safe for concurrent use and return a list
// the caller may keep: the evaluator does not copy it.
type Environment interface {
	Instancetype(name string) (ResourceList, error)
	Preset(name string) (ResourceList, error)
}

// StaticEnvironment is an Environment over fixed tables.
type StaticEnvironment struct {
	Instancetypes map[string]ResourceList
	Presets       map[string]ResourceList
}

var _ Environment = (*StaticEnvironment)(nil)

// NewStaticEnvironment classifies every VirtualMachineClusterInstancetype in
// instancetypes and pairs the result with presets. One unclassifiable type
// fails the whole environment, so that a broken catalog is noticed when it is
// loaded rather than when a tenant first orders that type.
func NewStaticEnvironment(instancetypes []map[string]any, opts InstancetypeOptions, presets map[string]ResourceList) (*StaticEnvironment, error) {
	env := &StaticEnvironment{
		Instancetypes: make(map[string]ResourceList, len(instancetypes)),
		Presets:       presets,
	}
	for _, obj := range instancetypes {
		rl, err := ClassifyInstancetype(obj, opts)
		if err != nil {
			return nil, err
		}
		name, _, _ := unstructured.NestedString(obj, "metadata", "name")
		if name == "" {
			return nil, fmt.Errorf("instance type without metadata.name")
		}
		env.Instancetypes[name] = rl
	}
	return env, nil
}

func (e *StaticEnvironment) Instancetype(name string) (ResourceList, error) {
	rl, ok := e.Instancetypes[name]
	if !ok {
		return nil, fmt.Errorf("instance type %q: %w", name, ErrNotFound)
	}
	return rl, nil
}

func (e *StaticEnvironment) Preset(name string) (ResourceList, error) {
	rl, ok := e.Presets[name]
	if !ok {
		return nil, fmt.Errorf("resource preset %q: %w", name, ErrNotFound)
	}
	return rl, nil
}
