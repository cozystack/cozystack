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

package webhook

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// The chart's webhook configurations must call the paths the binary serves.
func TestChartCallsRegisteredPaths(t *testing.T) {
	chart, err := os.ReadFile("../../../packages/system/cozystack-controller/templates/webhooks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{PathValidateDevice, PathMutateVMI, PathPinPod} {
		if !strings.Contains(string(chart), "path: "+path+"\n") {
			t.Errorf("webhooks.yaml does not call %s", path)
		}
	}
}

// vm-instance's chart tests pin the device annotation the chart renders;
// every pinned value must be one the webhook reads and admits.
func TestVMInstanceAnnotationsParse(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/apps/vm-instance/tests/gpus_test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		Tests []struct {
			It      string `json:"it"`
			Asserts []struct {
				Equal *struct {
					Path  string `json:"path"`
					Value any    `json:"value"`
				} `json:"equal"`
			} `json:"asserts"`
		} `json:"tests"`
	}
	if err := yaml.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, tc := range suite.Tests {
		for _, a := range tc.Asserts {
			if a.Equal == nil || a.Equal.Path != `spec.template.metadata.annotations["`+gpu.AnnotationDevices+`"]` {
				continue
			}
			found++
			value, ok := a.Equal.Value.(string)
			if !ok {
				t.Errorf("%s: annotation is not a string: %v", tc.It, a.Equal.Value)
				continue
			}
			refs, err := model.ParseDeviceRefs(value)
			if err != nil {
				t.Errorf("%s: the webhook cannot read %s: %v", tc.It, value, err)
				continue
			}
			u := vmi(t, vmiName, refs...)
			u.SetAnnotations(map[string]string{gpu.AnnotationDevices: value})
			if resp := admitVMI(t, newHandler(newClient(t)), u, true); strings.Contains(resp.Result.Message, gpu.AnnotationDevices) {
				t.Errorf("%s: the webhook refuses the annotation: %s", tc.It, resp.Result.Message)
			}
		}
	}
	if found < 3 {
		t.Fatalf("found %d pinned annotations in the vm-instance chart tests, want the held, template and mixed forms", found)
	}
}
