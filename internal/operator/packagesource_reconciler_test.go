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
	"strconv"
	"testing"
	"time"

	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// referenceTime is a fixed anchor for time-sensitive tests; every duration in
// the assertions is expressed as an offset from it so the tests read as
// "given the AG was last touched at reference-8s, and now is reference, ...".
var referenceTime = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

// agFixture builds an ArtifactGenerator with a fixed .metadata.generation and
// creation timestamp so tests can exercise the Generation and grace-period
// branches of artifactGeneratorStuck without a full apiserver round-trip.
func agFixture(gen int64, createdOffset time.Duration, status sourcewatcherv1beta1.ArtifactGeneratorStatus) *sourcewatcherv1beta1.ArtifactGenerator {
	return &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Generation:        gen,
			CreationTimestamp: metav1.NewTime(referenceTime.Add(createdOffset)),
		},
		Status: status,
	}
}

// TestArtifactGeneratorStuck locks in detection of the fluxcd/pkg#934 split
// -write stall. The predicate must fire ONLY when artifacts are demonstrably
// produced on the current generation AND the Ready condition has been sitting
// in Unknown longer than the grace period (or is missing on an old-enough AG).
// Ready=True and Ready=False must both pass through untouched — the retry
// driver only runs for the stuck-Unknown case, and a real regeneration failure
// (Ready=False) must never be papered over.
func TestArtifactGeneratorStuck(t *testing.T) {
	nonEmptyInventory := []sourcewatcherv1beta1.ExternalArtifactReference{
		{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
	}
	const observedDigest = "sha256:0e7"

	// beyondGrace: how far into Unknown the AG has been before we'll intervene.
	beyondGrace := stuckGracePeriod + 5*time.Second

	tests := []struct {
		name string
		ag   *sourcewatcherv1beta1.ArtifactGenerator
		want bool
	}{
		{
			name: "fresh ArtifactGenerator with nothing observed yet",
			ag:   agFixture(1, -time.Hour, sourcewatcherv1beta1.ArtifactGeneratorStatus{}),
			want: false,
		},
		{
			name: "inventory present but sources not yet observed",
			ag: agFixture(1, -time.Hour, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory: nonEmptyInventory,
			}),
			want: false,
		},
		{
			name: "sources observed but inventory empty",
			ag: agFixture(1, -time.Hour, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				ObservedSourcesDigest: observedDigest,
			}),
			want: false,
		},
		{
			name: "artifacts produced, Ready condition absent, AG within grace period — do NOT intervene yet",
			ag: agFixture(1, -5*time.Second, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
			}),
			want: false,
		},
		{
			name: "artifacts produced, Ready condition absent, AG older than grace period — intervene",
			ag: agFixture(1, -beyondGrace, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
			}),
			want: true,
		},
		{
			name: "artifacts produced, Ready=Unknown within grace period — legitimate in-flight rebuild, do NOT intervene",
			ag: agFixture(1, -time.Hour, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionUnknown,
					Reason:             "Progressing",
					Message:            "Reconciliation in progress",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.NewTime(referenceTime.Add(-5 * time.Second)),
				}},
			}),
			want: false,
		},
		{
			name: "artifacts produced, Ready=Unknown beyond grace period on current Generation — the stuck-status case",
			ag: agFixture(1, -time.Hour, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionUnknown,
					Reason:             "Progressing",
					Message:            "Reconciliation in progress",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.NewTime(referenceTime.Add(-beyondGrace)),
				}},
			}),
			want: true,
		},
		{
			name: "artifacts observed on a PRIOR Generation (spec was updated) — do NOT intervene, condition is legitimately stale",
			ag: agFixture(2, -time.Hour, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionUnknown,
					Reason:             "Progressing",
					Message:            "Reconciliation in progress",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.NewTime(referenceTime.Add(-beyondGrace)),
				}},
			}),
			want: false,
		},
		{
			name: "upstream Ready=True — pass through the real condition, do NOT intervene",
			ag: agFixture(1, -time.Hour, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "Succeeded",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.NewTime(referenceTime.Add(-beyondGrace)),
				}},
			}),
			want: false,
		},
		{
			name: "upstream Ready=False — real failure, must NOT be masked",
			ag: agFixture(1, -time.Hour, sourcewatcherv1beta1.ArtifactGeneratorStatus{
				Inventory:             nonEmptyInventory,
				ObservedSourcesDigest: observedDigest,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionFalse,
					Reason:             "SourceRefFailed",
					Message:            "cannot fetch source",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.NewTime(referenceTime.Add(-beyondGrace)),
				}},
			}),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready := meta.FindStatusCondition(tt.ag.Status.Conditions, "Ready")
			got := artifactGeneratorStuck(tt.ag, ready, referenceTime)
			if got != tt.want {
				t.Errorf("artifactGeneratorStuck() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDecideRequeue locks in the bounded-retry state machine that drives
// source-watcher out of the stuck window. First attempt bumps immediately; the
// N-th subsequent attempt waits backoffFor(N) since the last bump before firing
// again; after maxRequeueAttempts fruitless attempts we surface Ready=False.
func TestDecideRequeue(t *testing.T) {
	tests := []struct {
		name          string
		attempts      int
		lastRequeueAt time.Time
		now           time.Time
		wantAction    requeueAction
		wantWaitMin   time.Duration // wait must be > 0 and roughly this long; 0 means don't check
	}{
		{
			name:       "first-ever detection — bump immediately",
			attempts:   0,
			now:        referenceTime,
			wantAction: requeueActionBump,
		},
		{
			name:          "backoff not yet elapsed after attempt 1 — wait",
			attempts:      1,
			lastRequeueAt: referenceTime.Add(-10 * time.Second),
			now:           referenceTime,
			wantAction:    requeueActionWait,
			wantWaitMin:   19 * time.Second, // ~initialBackoff (30s) minus elapsed 10s
		},
		{
			name:          "backoff elapsed after attempt 1 — bump",
			attempts:      1,
			lastRequeueAt: referenceTime.Add(-45 * time.Second),
			now:           referenceTime,
			wantAction:    requeueActionBump,
		},
		{
			name:          "backoff after attempt 3 (2m) — wait",
			attempts:      3,
			lastRequeueAt: referenceTime.Add(-30 * time.Second),
			now:           referenceTime,
			wantAction:    requeueActionWait,
			wantWaitMin:   time.Minute,
		},
		{
			name:       "attempts exhausted — give up",
			attempts:   maxRequeueAttempts,
			now:        referenceTime,
			wantAction: requeueActionGiveUp,
		},
		{
			name:       "attempts exceeded — still give up",
			attempts:   maxRequeueAttempts + 3,
			now:        referenceTime,
			wantAction: requeueActionGiveUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideRequeue(tt.attempts, tt.lastRequeueAt, tt.now)
			if got.action != tt.wantAction {
				t.Fatalf("action = %v, want %v", got.action, tt.wantAction)
			}
			if tt.wantWaitMin > 0 && got.wait < tt.wantWaitMin {
				t.Errorf("wait = %v, want at least %v", got.wait, tt.wantWaitMin)
			}
		})
	}
}

// TestBackoffFor pins the exponential-with-cap schedule so a future edit that
// silently doubles the ceiling or drops the exponent gets caught here.
func TestBackoffFor(t *testing.T) {
	// initialBackoff=30s, maxBackoff=4m. Expected: 30s, 60s, 120s, 240s, 240s, 240s, ...
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 30 * time.Second},  // clamped to attempt=1
		{1, 30 * time.Second},
		{2, 60 * time.Second},
		{3, 120 * time.Second},
		{4, 240 * time.Second},
		{5, 240 * time.Second},
		{10, 240 * time.Second},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.attempt), func(t *testing.T) {
			got := backoffFor(tt.attempt)
			if got != tt.want {
				t.Errorf("backoffFor(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

// TestReadRequeueTracking verifies that a corrupted or malformed retry-tracking
// annotation cannot wedge the retry loop by producing a nonsensical counter —
// missing, empty, non-numeric, or negative values must all read back as
// "no prior attempts".
func TestReadRequeueTracking(t *testing.T) {
	fixed := referenceTime.UTC().Format(time.RFC3339Nano)

	tests := []struct {
		name        string
		annotations map[string]string
		wantAttempt int
		wantTimeSet bool
	}{
		{
			name:        "no annotations",
			annotations: nil,
			wantAttempt: 0,
			wantTimeSet: false,
		},
		{
			name: "valid attempt + valid timestamp",
			annotations: map[string]string{
				annotationRequeueAttempts: "3",
				annotationLastRequeueAt:   fixed,
			},
			wantAttempt: 3,
			wantTimeSet: true,
		},
		{
			name: "corrupt attempt counter — read as zero",
			annotations: map[string]string{
				annotationRequeueAttempts: "not-a-number",
				annotationLastRequeueAt:   fixed,
			},
			wantAttempt: 0,
			wantTimeSet: true,
		},
		{
			name: "negative attempt — clamped to zero",
			annotations: map[string]string{
				annotationRequeueAttempts: "-5",
			},
			wantAttempt: 0,
			wantTimeSet: false,
		},
		{
			name: "corrupt timestamp — read as zero time",
			annotations: map[string]string{
				annotationRequeueAttempts: "2",
				annotationLastRequeueAt:   "not-a-date",
			},
			wantAttempt: 2,
			wantTimeSet: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := &sourcewatcherv1beta1.ArtifactGenerator{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
			}
			attempt, ts := readRequeueTracking(ag)
			if attempt != tt.wantAttempt {
				t.Errorf("attempt = %d, want %d", attempt, tt.wantAttempt)
			}
			if tt.wantTimeSet && ts.IsZero() {
				t.Error("timestamp is zero, want set")
			}
			if !tt.wantTimeSet && !ts.IsZero() {
				t.Errorf("timestamp = %v, want zero", ts)
			}
		})
	}
}
