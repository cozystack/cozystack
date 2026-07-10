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

package config

import (
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

// ResolveWaitStrategy is the single source of truth for the wait-strategy value
// on both HelmRelease-generating paths (cozystack-api and cozystack-operator).
// The couple-the-default behavior (expressions imply poller) is what makes a
// package that sets only healthCheckExprs self-contained, so it is the part
// worth pinning against regression.
func TestResolveWaitStrategy(t *testing.T) {
	tests := []struct {
		name         string
		waitStrategy string
		hasExprs     bool
		want         *helmv2.WaitStrategyName // nil => expect nil *WaitStrategy
	}{
		{
			name:         "unset, no exprs -> nil (leave flux default)",
			waitStrategy: "",
			hasExprs:     false,
			want:         nil,
		},
		{
			name:         "unset, with exprs -> poller (coupled default)",
			waitStrategy: "",
			hasExprs:     true,
			want:         ptr(helmv2.WaitStrategyPoller),
		},
		{
			name:         "explicit poller is honored",
			waitStrategy: "poller",
			hasExprs:     true,
			want:         ptr(helmv2.WaitStrategyPoller),
		},
		{
			name:         "explicit legacy is honored even with exprs",
			waitStrategy: "legacy",
			hasExprs:     true,
			want:         ptr(helmv2.WaitStrategyLegacy),
		},
		{
			name:         "explicit legacy without exprs is honored",
			waitStrategy: "legacy",
			hasExprs:     false,
			want:         ptr(helmv2.WaitStrategyLegacy),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveWaitStrategy(tc.waitStrategy, tc.hasExprs)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("ResolveWaitStrategy(%q, %v) = %+v, want nil", tc.waitStrategy, tc.hasExprs, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ResolveWaitStrategy(%q, %v) = nil, want %q", tc.waitStrategy, tc.hasExprs, *tc.want)
			}
			if got.Name != *tc.want {
				t.Errorf("ResolveWaitStrategy(%q, %v).Name = %q, want %q", tc.waitStrategy, tc.hasExprs, got.Name, *tc.want)
			}
		})
	}
}

func ptr(n helmv2.WaitStrategyName) *helmv2.WaitStrategyName { return &n }
