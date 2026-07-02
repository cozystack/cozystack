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

// agWithGen builds an ArtifactGenerator with a fixed .metadata.generation so
// tests can exercise the Generation-matching branch of
// artifactGeneratorObservablyReady without dragging in a full apiserver
// round-trip.
func agWithGen(gen int64, status sourcewatcherv1beta1.ArtifactGeneratorStatus) *sourcewatcherv1beta1.ArtifactGenerator {
	return &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{Generation: gen},
		Status:     status,
	}
}

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
			ag:   agWithGen(1, sourcewatcherv1beta1.ArtifactGeneratorStatus{}),
			want: false,
		},
		{
			name: "inventory present but sources not yet observed",
			ag: agWithGen(1, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory: nonEmptyInventory,
			}),
			want: false,
		},
		{
			name: "sources observed but inventory empty",
			ag: agWithGen(1, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				ObservedSourcesDigest: observedDigest,
			}),
			want: false,
		},
		{
			name: "artifacts fully produced but Ready condition absent — cannot synthesise without an ObservedGeneration to verify",
			ag: agWithGen(1, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
			}),
			want: false,
		},
		{
			name: "artifacts fully produced AND Ready=Unknown observed on current Generation — the actual stuck-status case",
			ag: agWithGen(1, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionUnknown,
					Reason:             "Progressing",
					Message:            "Reconciliation in progress",
					ObservedGeneration: 1,
				}},
			}),
			want: true,
		},
		{
			name: "artifacts observed AND Ready=Unknown but on a PRIOR Generation — spec was updated, artifacts are stale, do NOT synthesise",
			ag: agWithGen(2, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionUnknown,
					Reason:             "Progressing",
					Message:            "Reconciliation in progress",
					ObservedGeneration: 1,
				}},
			}),
			want: false,
		},
		{
			name: "upstream Ready=True — pass through the real condition, do NOT synthesise",
			ag: agWithGen(1, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "Succeeded",
					ObservedGeneration: 1,
				}},
			}),
			want: false,
		},
		{
			name: "upstream Ready=False — real failure, must NOT be masked as Ready",
			ag: agWithGen(1, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionFalse,
					Reason:             "SourceRefFailed",
					Message:            "cannot fetch source",
					ObservedGeneration: 1,
				}},
			}),
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
