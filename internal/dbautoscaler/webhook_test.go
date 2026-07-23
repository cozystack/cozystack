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

package dbautoscaler

import "testing"

const (
	operatorUser = "system:serviceaccount:cozy-db-autoscaler:db-autoscaler"
	appsAPIUser  = "system:serviceaccount:cozy-system:cozystack-api"
)

var allowedUsers = []string{operatorUser, appsAPIUser}

// helmReleaseJSON builds a backing HelmRelease with the projected marker and
// spec.values.replicas, matching what the aggregated apps API writes.
func helmReleaseJSON(marker string, replicas int) []byte {
	ann := ""
	if marker != "" {
		ann = `"annotations":{"` + ProjectedMarkerAnnotation + `":"` + marker + `"},`
	}
	return []byte(`{"metadata":{` + ann + `"name":"postgres-db"},"spec":{"values":{"replicas":` + itoa(replicas) + `}}}`)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestEvaluateOwnership(t *testing.T) {
	path := []string{"values", "replicas"}
	tests := []struct {
		name        string
		old, new    []byte
		user        string
		wantAllowed bool
	}{
		{
			name:        "unmanaged release: allow any writer",
			old:         helmReleaseJSON("", 3),
			new:         helmReleaseJSON("", 5),
			user:        "system:serviceaccount:tenant:flux",
			wantAllowed: true,
		},
		{
			name:        "managed, replicas unchanged: allow",
			old:         helmReleaseJSON("db", 3),
			new:         helmReleaseJSON("db", 3),
			user:        "system:serviceaccount:tenant:flux",
			wantAllowed: true,
		},
		{
			name:        "managed, replicas changed by a direct Flux writer: deny",
			old:         helmReleaseJSON("db", 3),
			new:         helmReleaseJSON("db", 5),
			user:        "system:serviceaccount:kube-system:kustomize-controller",
			wantAllowed: false,
		},
		{
			name:        "managed, changed by the operator: allow",
			old:         helmReleaseJSON("db", 3),
			new:         helmReleaseJSON("db", 5),
			user:        operatorUser,
			wantAllowed: true,
		},
		{
			name:        "managed, changed via the apps API (extension server): allow",
			old:         helmReleaseJSON("db", 3),
			new:         helmReleaseJSON("db", 5),
			user:        appsAPIUser,
			wantAllowed: true,
		},
		{
			name:        "managed, changed by a human writing the HelmRelease directly: deny",
			old:         helmReleaseJSON("db", 3),
			new:         helmReleaseJSON("db", 2),
			user:        "kubernetes-admin",
			wantAllowed: false,
		},
		{
			name:        "create (no old object): allow",
			old:         nil,
			new:         helmReleaseJSON("db", 3),
			user:        "system:serviceaccount:tenant:flux",
			wantAllowed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, msg := evaluateOwnership(tt.old, tt.new, tt.user, ProjectedMarkerAnnotation, path, allowedUsers)
			if allowed != tt.wantAllowed {
				t.Fatalf("allowed = %v (msg=%q), want %v", allowed, msg, tt.wantAllowed)
			}
			if !allowed && msg == "" {
				t.Fatalf("deny should carry a message")
			}
		})
	}
}
