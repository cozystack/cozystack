/*
Copyright 2025 The Cozystack Authors.

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

package operator

import (
	"testing"

	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestArtifactGeneratorObservablyReady locks in the workaround for the
// fluxcd/source-watcher status-patch early-exit bug. The predicate must be
// selective enough that:
//
//  1. it fires ONLY when artifacts are demonstrably present (Inventory
//     non-empty AND ObservedSourcesDigest set) — otherwise a fresh
//     ArtifactGenerator that has literally not started work would be
//     synthesised Ready and downstream reconciles would race the real
//     artifacts,
//  2. it does NOT fire when the upstream Ready condition has resolved
//     (True or False) — True must pass through unchanged, False must
//     surface as a real failure and not be masked.
//
// This regression-tests both the stuck-status case (the bug we are
// papering over) and every case that must NOT trigger it.
func TestArtifactGeneratorObservablyReady(t *testing.T) {
	nonEmptyInventory := []sourcewatcherv1beta1.ExternalArtifactReference{
		{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
	}
	const observedDigest = "sha256:0e7"

	tests := []struct {
		name string
		ag   *sourcewatcherv1beta1.ArtifactGenerator
		want bool
	}{
		{
			name: "fresh ArtifactGenerator with nothing observed yet",
			ag:   &sourcewatcherv1beta1.ArtifactGenerator{},
			want: false,
		},
		{
			name: "inventory present but sources not yet observed",
			ag: &sourcewatcherv1beta1.ArtifactGenerator{
				Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
					Inventory: nonEmptyInventory,
				},
			},
			want: false,
		},
		{
			name: "sources observed but inventory empty",
			ag: &sourcewatcherv1beta1.ArtifactGenerator{
				Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
					ObservedSourcesDigest: observedDigest,
				},
			},
			want: false,
		},
		{
			name: "artifacts fully produced but Ready condition still Unknown — the actual stuck-status case",
			ag: &sourcewatcherv1beta1.ArtifactGenerator{
				Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
					Inventory:             nonEmptyInventory,
					ObservedSourcesDigest: observedDigest,
					Conditions: []metav1.Condition{{
						Type:    "Ready",
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					}},
				},
			},
			want: true,
		},
		{
			name: "artifacts fully produced and no Ready condition at all — same stuck window before source-watcher stamps anything",
			ag: &sourcewatcherv1beta1.ArtifactGenerator{
				Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
					Inventory:             nonEmptyInventory,
					ObservedSourcesDigest: observedDigest,
				},
			},
			want: true,
		},
		{
			name: "upstream Ready=True — pass through the real condition, do NOT synthesise",
			ag: &sourcewatcherv1beta1.ArtifactGenerator{
				Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
					Inventory:             nonEmptyInventory,
					ObservedSourcesDigest: observedDigest,
					Conditions: []metav1.Condition{{
						Type:   "Ready",
						Status: metav1.ConditionTrue,
						Reason: "Succeeded",
					}},
				},
			},
			want: false,
		},
		{
			name: "upstream Ready=False — real failure, must NOT be masked as Ready",
			ag: &sourcewatcherv1beta1.ArtifactGenerator{
				Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
					Inventory:             nonEmptyInventory,
					ObservedSourcesDigest: observedDigest,
					Conditions: []metav1.Condition{{
						Type:    "Ready",
						Status:  metav1.ConditionFalse,
						Reason:  "SourceRefFailed",
						Message: "cannot fetch source",
					}},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := artifactGeneratorObservablyReady(tt.ag)
			if got != tt.want {
				t.Errorf("artifactGeneratorObservablyReady() = %v, want %v", got, tt.want)
			}
		})
	}
}
