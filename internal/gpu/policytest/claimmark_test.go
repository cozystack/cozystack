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

package policytest

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cozystack/cozystack/internal/gpu"
)

// The order webhook skips its budget and capacity checks for any claim
// carrying the template-claim label, whatever its value, so the tamper
// policy must refuse the label from a tenant whatever its value too.
func TestClaimTamperRefusesEveryMarkValue(t *testing.T) {
	v := compile(t, get(t, claimPolicyFile, claimTamper).policy)
	held := []string{gpu.FinalizerClaim}
	for _, value := range []string{gpu.LabelValueTrue, "false", ""} {
		mark := map[string]string{gpu.LabelTemplateClaim: value}
		for name, c := range map[string]struct{ old, new *unstructured.Unstructured }{
			"create": {new: claim(nil, mark)},
			"add":    {old: claim(held, nil), new: claim(held, mark)},
			"change": {old: claim(held, map[string]string{gpu.LabelTemplateClaim: "other"}), new: claim(held, mark)},
		} {
			t.Run(name+"/"+value, func(t *testing.T) {
				denials := evaluate(t, v, request{gvk: claimGVK, resource: "gpuclaims", namespace: "tenant-acme", obj: c.new, old: c.old, user: tenantUser})
				if !slices.ContainsFunc(denials, func(m string) bool { return strings.Contains(m, gpu.LabelTemplateClaim) }) {
					t.Fatalf("a tenant set the template mark to %q on %s: %v", value, name, denials)
				}
			})
		}
	}
}
