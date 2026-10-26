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

	"github.com/cozystack/cozystack/internal/gpu"
)

// The chart's webhook configurations must call the paths the binary
// serves and select on the keys it reads.
func TestChartMatchesTheHandlers(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/system/cozystack-controller/templates/webhooks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	chart := string(raw)
	for _, want := range []string{
		"path: " + PathValidateClaim + "\n",
		"path: " + PathMutateVMI + "\n",
		"path: " + PathGatePod + "\n",
		gpu.LabelClaimReference + `: "true"`,
		labelKubeVirt + ": " + valueVirtLauncher,
		"key: " + labelMigrationJob + "\n",
	} {
		if !strings.Contains(chart, want) {
			t.Errorf("webhooks.yaml lacks %q", want)
		}
	}
}
