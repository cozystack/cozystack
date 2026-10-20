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
