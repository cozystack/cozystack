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

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
)

// ClassEntry is one entry of the cozystack.io/gpus annotation: count GPUs
// of a class.
type ClassEntry struct {
	ClassName string `json:"className"`
	Count     int32  `json:"count"`
}

// ParseClassEntries reads the cozystack.io/gpus annotation. Count defaults
// to 1. An empty list, an unknown field or an entry without a valid class
// name is an error, so a mistyped annotation never reads as no GPUs.
func ParseClassEntries(raw string) ([]ClassEntry, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var entries []struct {
		ClassName string `json:"className"`
		Count     *int32 `json:"count"`
	}
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("no entries")
	}
	out := make([]ClassEntry, 0, len(entries))
	for i, e := range entries {
		if errs := validation.IsDNS1123Label(e.ClassName); len(errs) > 0 {
			return nil, fmt.Errorf("entry %d: className %q: %s", i, e.ClassName, strings.Join(errs, "; "))
		}
		count := int32(1)
		if e.Count != nil {
			count = *e.Count
		}
		if count < 1 {
			return nil, fmt.Errorf("entry %d: count %d is below 1", i, count)
		}
		out = append(out, ClassEntry{ClassName: e.ClassName, Count: count})
	}
	return out, nil
}

// GPUResourceNames is the set of GPU resource names: the hoster's list,
// the classes' resource names and the GPU names of the KubeVirt
// permittedHostDevices table. None of them replaces another.
func GPUResourceNames(hoster []string, classes []Class, table []string) sets.Set[string] {
	out := sets.New[string]()
	for _, n := range hoster {
		out.Insert(n)
	}
	for _, c := range classes {
		out.Insert(string(c.ResourceName))
	}
	for _, n := range table {
		out.Insert(n)
	}
	out.Delete("")
	return out
}
