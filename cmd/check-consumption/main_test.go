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

package main

import (
	"bytes"
	"strings"
	"testing"
)

const instancetypeDoc = `
apiVersion: instancetype.kubevirt.io/v1beta1
kind: VirtualMachineClusterInstancetype
metadata:
  name: u1.small
spec:
  cpu:
    guest: 1
  memory:
    guest: 2Gi
`

func definition(name, consumption string) string {
	return `
apiVersion: cozystack.io/v1alpha1
kind: ApplicationDefinition
metadata:
  name: ` + name + `
spec:
  application:
    kind: X
    openAPISchema: '{"type":"object","properties":{"instanceType":{"type":"string","default":"u1.small"},"preset":{"type":"string","default":"t1.micro"}}}'
` + consumption
}

func stream(docs ...string) string {
	return "---" + strings.Join(docs, "---")
}

func TestCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		in string
		// fail is a substring the error must carry; empty means no error.
		fail string
		// out is a substring the report must carry, absent one it must not.
		out, absent string
	}{
		"evaluates on the rendered instance types": {
			in: stream(instancetypeDoc, definition("vm", "  consumption:\n    resources:\n      cpu: instanceType(values.instanceType).cpu\n")),
		},
		"a definition without a declaration is not checked": {
			in:     stream(definition("plain", "")),
			absent: "plain",
		},
		"a schema that does not build": {
			in:   stream(strings.Replace(definition("bad-schema", "  consumption:\n    resources:\n      cpu: \"1\"\n"), `'{"type"`, `'{"type":`, 1)),
			fail: "bad-schema",
		},
		"a stream that is not YAML": {
			in:   "kind: [\n",
			fail: "yaml",
		},
		"a declaration that does not compile": {
			in:   stream(definition("broken", "  consumption:\n    resources:\n      cpu: nosuchfunction(values.instanceType)\n")),
			fail: "broken",
		},
		"an instance type the stream does not render": {
			in:   stream(definition("vm", "  consumption:\n    resources:\n      cpu: instanceType(values.instanceType).cpu\n")),
			fail: "u1.small",
		},
		"an instance type the classifier rejects": {
			in:   stream("\nkind: VirtualMachineClusterInstancetype\nmetadata:\n  name: bad\nspec:\n  cpu:\n    guest: 1\n"),
			fail: "bad",
		},
		"presets are not rendered, so a preset lookup ends the evaluation": {
			in:  stream(definition("db", "  consumption:\n    resources:\n      cpu: preset(values.preset).cpu\n")),
			out: "db: compiled; evaluation on defaults stopped at a preset lookup",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := check(strings.NewReader(tc.in), &out)
			switch {
			case tc.fail == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.fail != "" && (err == nil || !strings.Contains(err.Error(), tc.fail)):
				t.Fatalf("got %v, want an error carrying %q", err, tc.fail)
			}
			if !strings.Contains(out.String(), tc.out) {
				t.Fatalf("report %q does not carry %q", out.String(), tc.out)
			}
			if tc.absent != "" && strings.Contains(out.String(), tc.absent) {
				t.Fatalf("report %q carries %q", out.String(), tc.absent)
			}
		})
	}
}
