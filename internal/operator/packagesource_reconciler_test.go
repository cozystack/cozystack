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
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

// TestDecideRecovery locks in the bounded-retry state machine that drives
// source-watcher out of the stuck window. First attempt forces immediately;
// the N-th subsequent attempt waits backoffFor(N) since the last force before
// firing again; after maxRecoveryAttempts fruitless attempts we surface
// Ready=False. Wait durations are asserted for exact equality because
// decideRecovery is deterministic (`needed - elapsed`) — a loose bound would
// silently miss a backoff-schedule regression.
func TestDecideRecovery(t *testing.T) {
	tests := []struct {
		name           string
		attempts       int
		lastRecoveryAt time.Time
		now            time.Time
		wantAction     recoveryAction
		wantWait       time.Duration // 0 means don't check (Force / GiveUp branches ignore wait)
	}{
		{
			name:       "first-ever detection — force immediately",
			attempts:   0,
			now:        referenceTime,
			wantAction: recoveryActionForce,
		},
		{
			name:           "backoff not yet elapsed after attempt 1 — wait",
			attempts:       1,
			lastRecoveryAt: referenceTime.Add(-10 * time.Second),
			now:            referenceTime,
			wantAction:     recoveryActionWait,
			wantWait:       20 * time.Second, // initialBackoff (30s) minus elapsed 10s
		},
		{
			name:           "backoff elapsed after attempt 1 — force",
			attempts:       1,
			lastRecoveryAt: referenceTime.Add(-45 * time.Second),
			now:            referenceTime,
			wantAction:     recoveryActionForce,
		},
		{
			name:           "backoff after attempt 3 (2m) — wait 90s",
			attempts:       3,
			lastRecoveryAt: referenceTime.Add(-30 * time.Second),
			now:            referenceTime,
			wantAction:     recoveryActionWait,
			wantWait:       90 * time.Second, // backoffFor(3)=2m minus elapsed 30s
		},
		{
			name:       "attempts exhausted — give up",
			attempts:   maxRecoveryAttempts,
			now:        referenceTime,
			wantAction: recoveryActionGiveUp,
		},
		{
			name:       "attempts exceeded — still give up",
			attempts:   maxRecoveryAttempts + 3,
			now:        referenceTime,
			wantAction: recoveryActionGiveUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideRecovery(tt.attempts, tt.lastRecoveryAt, tt.now)
			if got.action != tt.wantAction {
				t.Fatalf("action = %v, want %v", got.action, tt.wantAction)
			}
			if tt.wantWait > 0 && got.wait != tt.wantWait {
				t.Errorf("wait = %v, want %v", got.wait, tt.wantWait)
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

// TestReadRecoveryTracking verifies that a corrupted or malformed retry-tracking
// annotation cannot wedge the retry loop by producing a nonsensical counter —
// missing, empty, non-numeric, or negative values must all read back as
// "no prior attempts".
func TestReadRecoveryTracking(t *testing.T) {
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
				annotationRecoveryAttempts: "3",
				annotationLastRecoveryAt:   fixed,
			},
			wantAttempt: 3,
			wantTimeSet: true,
		},
		{
			name: "corrupt attempt counter — read as zero",
			annotations: map[string]string{
				annotationRecoveryAttempts: "not-a-number",
				annotationLastRecoveryAt:   fixed,
			},
			wantAttempt: 0,
			wantTimeSet: true,
		},
		{
			name: "negative attempt — clamped to zero",
			annotations: map[string]string{
				annotationRecoveryAttempts: "-5",
			},
			wantAttempt: 0,
			wantTimeSet: false,
		},
		{
			name: "corrupt timestamp — read as zero time",
			annotations: map[string]string{
				annotationRecoveryAttempts: "2",
				annotationLastRecoveryAt:   "not-a-date",
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
			attempt, ts := readRecoveryTracking(ag)
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

// TestCloneAnnotations pins the "shallow copy, nil in / nil out" contract that
// the rollback path in forceArtifactGeneratorDrift / clearRecoveryTracking
// depends on. If a future refactor "normalises" nil to an empty map, the
// rollback would leave the caller with an empty map instead of the original
// nil, subtly changing observable state in downstream code that treats
// annotations == nil as a distinct signal.
func TestCloneAnnotations(t *testing.T) {
	t.Run("nil in, nil out", func(t *testing.T) {
		if got := cloneAnnotations(nil); got != nil {
			t.Errorf("cloneAnnotations(nil) = %v, want nil", got)
		}
	})
	t.Run("empty in, non-nil empty out", func(t *testing.T) {
		got := cloneAnnotations(map[string]string{})
		if got == nil {
			t.Error("cloneAnnotations(empty) returned nil, want empty map")
		}
		if len(got) != 0 {
			t.Errorf("cloneAnnotations(empty) len = %d, want 0", len(got))
		}
	})
	t.Run("populated in, independent copy out", func(t *testing.T) {
		src := map[string]string{"a": "1", "b": "2"}
		got := cloneAnnotations(src)
		if len(got) != 2 || got["a"] != "1" || got["b"] != "2" {
			t.Errorf("cloneAnnotations(populated) = %v, want copy of %v", got, src)
		}
		// Mutation-independence: modifying the source must not touch the copy.
		src["a"] = "changed"
		if got["a"] != "1" {
			t.Errorf("clone was aliased to source; got[a]=%q after src[a]=changed", got["a"])
		}
	})
}

// testScheme is a shared scheme with the CRDs both reconciler test suites need.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("cozyv1alpha1.AddToScheme: %v", err)
	}
	if err := sourcewatcherv1beta1.AddToScheme(s); err != nil {
		t.Fatalf("sourcewatcherv1beta1.AddToScheme: %v", err)
	}
	return s
}

// newAG builds a minimal ArtifactGenerator suitable for fake-client tests.
func newAG(annotations map[string]string) *sourcewatcherv1beta1.ArtifactGenerator {
	return &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "example",
			Namespace:   "cozy-system",
			Annotations: annotations,
		},
	}
}

// TestForceArtifactGeneratorDrift_Success asserts that on a successful
// force-drift call:
//   - the AG's status.conditions[Ready] is set to False in the apiserver (this
//     is the signal source-watcher's detectDrift keys on),
//   - the tracking annotations land on the caller's `ag` AND in the apiserver,
//   - the same in-memory `ag` reflects both mutations (mutation contract).
//
// This locks in the two-subresource shape (status patch + metadata patch) and
// the mutation contract documented on the function.
func TestForceArtifactGeneratorDrift_Success(t *testing.T) {
	ag := newAG(nil)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ag).WithObjects(ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	if err := r.forceArtifactGeneratorDrift(context.Background(), ag, referenceTime, 2); err != nil {
		t.Fatalf("forceArtifactGeneratorDrift: %v", err)
	}

	// In-memory ag: Ready=False + tracking annotations reflect the writes.
	ready := meta.FindStatusCondition(ag.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonRecoveryForced {
		t.Fatalf("ag.Status Ready condition = %+v, want False/%s", ready, reasonRecoveryForced)
	}
	if got := ag.Annotations[annotationRecoveryAttempts]; got != "2" {
		t.Errorf("ag.Annotations[%s] = %q, want 2", annotationRecoveryAttempts, got)
	}
	if got := ag.Annotations[annotationLastRecoveryAt]; got == "" {
		t.Errorf("ag.Annotations[%s] not set", annotationLastRecoveryAt)
	}
	// B1 (lexfrei): the requestedAt annotation MUST also be set — it is the
	// signal source-watcher's ReconcileRequestedPredicate keys on to enqueue
	// a reconcile. Without it the status patch alone is a no-op for up to
	// the AG's 1h self-requeue interval.
	if got := ag.Annotations[annotationFluxRequestedAt]; got == "" {
		t.Errorf("ag.Annotations[%s] not set — source-watcher will not enqueue a reconcile", annotationFluxRequestedAt)
	}

	// apiserver reflects both writes.
	persisted := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ag), persisted); err != nil {
		t.Fatalf("Get after force-drift: %v", err)
	}
	persistedReady := meta.FindStatusCondition(persisted.Status.Conditions, "Ready")
	if persistedReady == nil || persistedReady.Status != metav1.ConditionFalse {
		t.Errorf("persisted AG Ready condition = %+v, want False", persistedReady)
	}
	for _, k := range []string{annotationFluxRequestedAt, annotationRecoveryAttempts, annotationLastRecoveryAt} {
		if _, ok := persisted.Annotations[k]; !ok {
			t.Errorf("persisted AG missing annotation %s", k)
		}
	}
}

// TestForceArtifactGeneratorDrift_StatusPatchFailure_Rollback drives the
// status-patch failure branch: an interceptor errors on SubResourcePatch.
// The caller's `ag` must be untouched (both conditions and annotations) and
// no metadata patch should have been issued.
func TestForceArtifactGeneratorDrift_StatusPatchFailure_Rollback(t *testing.T) {
	ag := newAG(map[string]string{"unrelated": "value"})
	baseClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ag).WithObjects(ag).Build()
	metadataPatches := 0
	failing := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			metadataPatches++
			return c.Patch(ctx, obj, patch, opts...)
		},
		SubResourcePatch: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
			return errors.New("simulated status patch failure")
		},
	})
	r := &PackageSourceReconciler{Client: failing, Scheme: testScheme(t)}

	if err := r.forceArtifactGeneratorDrift(context.Background(), ag, referenceTime, 3); err == nil {
		t.Fatal("forceArtifactGeneratorDrift succeeded, want error")
	}
	if ready := meta.FindStatusCondition(ag.Status.Conditions, "Ready"); ready != nil {
		t.Errorf("ag.Status Ready condition = %+v after failed status patch, want unset (rolled back)", ready)
	}
	if _, ok := ag.Annotations[annotationRecoveryAttempts]; ok {
		t.Errorf("tracking annotation leaked despite status-patch failure: %v", ag.Annotations)
	}
	if metadataPatches != 0 {
		t.Errorf("metadata patch issued %d times despite status-patch failure, want 0", metadataPatches)
	}
	if ag.Annotations["unrelated"] != "value" {
		t.Errorf("pre-existing annotation lost: %v", ag.Annotations)
	}
}

// TestForceArtifactGeneratorDrift_MetadataPatchFailure_RollbackAnnotations
// drives the second-step failure: status patch succeeds, metadata patch fails.
// Ready=False must remain in the caller's `ag` (successfully persisted) but
// the tracking annotations must roll back to pre-call state so the counter
// doesn't advance without landing in the apiserver.
func TestForceArtifactGeneratorDrift_MetadataPatchFailure_RollbackAnnotations(t *testing.T) {
	ag := newAG(map[string]string{"unrelated": "value"})
	baseClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ag).WithObjects(ag).Build()
	failing := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return errors.New("simulated metadata patch failure")
		},
	})
	r := &PackageSourceReconciler{Client: failing, Scheme: testScheme(t)}

	if err := r.forceArtifactGeneratorDrift(context.Background(), ag, referenceTime, 4); err == nil {
		t.Fatal("forceArtifactGeneratorDrift succeeded, want error from metadata patch")
	}
	// Status patch already landed, so ag.Status.Conditions carries Ready=False
	// even though the operation as a whole errored.
	ready := meta.FindStatusCondition(ag.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Errorf("ag.Status Ready condition = %+v, want False (status patch succeeded)", ready)
	}
	// Annotations must roll back — the counter never advanced in apiserver so
	// leaving it locally advanced would desync the caller's view. Both the
	// tracking annotations AND the requestedAt bump (added for B1) must be
	// symmetrically rolled back — otherwise source-watcher would see a bumped
	// requestedAt with no matching backoff-state on our side.
	if _, ok := ag.Annotations[annotationRecoveryAttempts]; ok {
		t.Errorf("tracking annotation not rolled back after metadata patch failure: %v", ag.Annotations)
	}
	if _, ok := ag.Annotations[annotationFluxRequestedAt]; ok {
		t.Errorf("requestedAt annotation not rolled back after metadata patch failure: %v", ag.Annotations)
	}
	if ag.Annotations["unrelated"] != "value" {
		t.Errorf("pre-existing annotation lost: %v", ag.Annotations)
	}
}

// TestForceArtifactGeneratorDrift_MetadataPatchFailure_RollbackRestoresPriorBump
// exercises the subtle case where prior tracking annotations were set by an
// EARLIER force-drift and the current metadata patch fails. Rollback must
// restore prior values, not delete them.
func TestForceArtifactGeneratorDrift_MetadataPatchFailure_RollbackRestoresPriorBump(t *testing.T) {
	priorTimestamp := referenceTime.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	ag := newAG(map[string]string{
		annotationRecoveryAttempts: "1",
		annotationLastRecoveryAt:   priorTimestamp,
	})
	baseClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ag).WithObjects(ag).Build()
	failing := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return errors.New("simulated metadata patch failure")
		},
	})
	r := &PackageSourceReconciler{Client: failing, Scheme: testScheme(t)}

	if err := r.forceArtifactGeneratorDrift(context.Background(), ag, referenceTime, 2); err == nil {
		t.Fatal("force-drift succeeded, want error")
	}
	if got := ag.Annotations[annotationRecoveryAttempts]; got != "1" {
		t.Errorf("annotationRecoveryAttempts = %q after rollback, want prior value 1", got)
	}
	if got := ag.Annotations[annotationLastRecoveryAt]; got != priorTimestamp {
		t.Errorf("annotationLastRecoveryAt = %q after rollback, want prior %q", got, priorTimestamp)
	}
}

// TestClearRecoveryTracking_Noop asserts that if the AG has no tracking
// annotations, we don't issue a Patch at all — otherwise every healthy
// reconcile would generate a no-op write.
func TestClearRecoveryTracking_Noop(t *testing.T) {
	ag := newAG(map[string]string{"unrelated": "value"})
	baseClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(ag).Build()
	patchCount := 0
	watched := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patchCount++
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	r := &PackageSourceReconciler{Client: watched, Scheme: testScheme(t)}

	if err := r.clearRecoveryTracking(context.Background(), ag); err != nil {
		t.Fatalf("clearRecoveryTracking: %v", err)
	}
	if patchCount != 0 {
		t.Errorf("clearRecoveryTracking issued %d Patch calls, want 0", patchCount)
	}
}

// TestClearRecoveryTracking_PatchFailure_Rollback drives the failure branch and
// asserts the tracking annotations are restored on the caller's `ag`.
func TestClearRecoveryTracking_PatchFailure_Rollback(t *testing.T) {
	original := map[string]string{
		annotationRecoveryAttempts: "3",
		annotationLastRecoveryAt:   referenceTime.UTC().Format(time.RFC3339Nano),
		"unrelated":               "value",
	}
	ag := newAG(cloneAnnotations(original))
	baseClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(ag).Build()
	failing := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return errors.New("simulated Patch failure")
		},
	})
	r := &PackageSourceReconciler{Client: failing, Scheme: testScheme(t)}

	if err := r.clearRecoveryTracking(context.Background(), ag); err == nil {
		t.Fatal("clearRecoveryTracking succeeded, want error")
	}
	if len(ag.Annotations) != len(original) {
		t.Fatalf("ag.Annotations size = %d, want %d after rollback", len(ag.Annotations), len(original))
	}
	for k, v := range original {
		if ag.Annotations[k] != v {
			t.Errorf("ag.Annotations[%s] = %q, want %q after rollback", k, ag.Annotations[k], v)
		}
	}
}

// TestReconcile_DeletionTimestamp_EarlyReturn asserts that a PackageSource
// carrying a deletionTimestamp does not have its status touched and does not
// trigger any writes to its ArtifactGenerator's annotations — the retry driver
// must not keep chasing an object that's already on its way out.
func TestReconcile_DeletionTimestamp_EarlyReturn(t *testing.T) {
	now := metav1.NewTime(referenceTime)
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "doomed",
			Namespace:         "cozy-system",
			DeletionTimestamp: &now,
			Finalizers:        []string{"cozystack.io/pretend"},
		},
		Spec: cozyv1alpha1.PackageSourceSpec{},
	}
	ag := newAG(map[string]string{annotationRecoveryAttempts: "2"})

	writes := 0
	base := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(ps, ag).Build()
	watched := interceptor.NewClient(base, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			writes++
			return c.Patch(ctx, obj, patch, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			writes++
			return c.Update(ctx, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			writes++
			return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			writes++
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	})
	r := &PackageSourceReconciler{Client: watched, Scheme: testScheme(t)}

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: ps.Name, Namespace: ps.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Reconcile scheduled RequeueAfter=%v on a deleted object, want 0", res.RequeueAfter)
	}
	if writes != 0 {
		t.Errorf("Reconcile issued %d writes on a deleted object, want 0", writes)
	}
}

// TestUpdateStatus_UnknownWithinGrace_SchedulesFollowUp locks in the fix for
// the dormancy bug caught on dev3: when Ready=Unknown but grace hasn't elapsed
// yet, artifactGeneratorStuck returns false, we fall through to the copy path
// and — without an explicit RequeueAfter — the reconciler would sleep until
// the AG's next status change (which may never come if it's exactly the
// split-patch race we exist to fix) or the 10h informer resync. This test
// asserts we schedule a follow-up reconcile at grace-period expiry so the
// recovery driver can take over.
func TestUpdateStatus_UnknownWithinGrace_SchedulesFollowUp(t *testing.T) {
	// Ready=Unknown, transitioned 10s ago — grace is 30s, so 20s remaining.
	transitionAt := referenceTime.Add(-10 * time.Second)
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{
				Name:      "src",
				Kind:      "OCIRepository",
				Namespace: "cozy-system",
			},
		},
	}
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "cozy-system", Generation: 1,
			CreationTimestamp: metav1.NewTime(referenceTime.Add(-time.Hour)),
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
			Inventory: []sourcewatcherv1beta1.ExternalArtifactReference{
				{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
			},
			ObservedSourcesDigest: "sha256:0e7",
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionUnknown,
				Reason:             "Progressing",
				Message:            "Reconciliation in progress",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.NewTime(transitionAt),
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	res, err := r.updateStatus(context.Background(), ps, referenceTime)
	if err != nil {
		t.Fatalf("updateStatus: %v", err)
	}
	// remaining = transitionAt + stuckGracePeriod - now = -10s + 30s = 20s
	if res.RequeueAfter != 20*time.Second {
		t.Errorf("RequeueAfter = %v, want 20s (remaining grace after 10s elapsed)", res.RequeueAfter)
	}
}

// TestUpdateStatus_MissingReadyWithinGrace_SchedulesFollowUp — same dormancy
// fix but for the ready==nil branch: a fresh AG with no Ready condition yet
// must also schedule a follow-up so the operator notices when grace elapses.
func TestUpdateStatus_MissingReadyWithinGrace_SchedulesFollowUp(t *testing.T) {
	createdAt := referenceTime.Add(-5 * time.Second) // 25s remaining on grace
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{
				Name:      "src",
				Kind:      "OCIRepository",
				Namespace: "cozy-system",
			},
		},
	}
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "cozy-system", Generation: 1,
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{}, // no Ready
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	res, err := r.updateStatus(context.Background(), ps, referenceTime)
	if err != nil {
		t.Fatalf("updateStatus: %v", err)
	}
	if res.RequeueAfter != 25*time.Second {
		t.Errorf("RequeueAfter = %v, want 25s", res.RequeueAfter)
	}
}

// TestAgFollowUpDelay pins the follow-up-delay computation, including the
// one-second floor that prevents a zero-delay tight loop when the anchor is
// right at or beyond grace expiry.
func TestAgFollowUpDelay(t *testing.T) {
	tests := []struct {
		name         string
		anchorOffset time.Duration
		want         time.Duration
	}{
		{"anchor 10s ago — grace has 20s left", -10 * time.Second, 20 * time.Second},
		{"anchor 5s ago — grace has 25s left", -5 * time.Second, 25 * time.Second},
		{"anchor right at grace expiry — floor to 1s", -stuckGracePeriod, time.Second},
		{"anchor past grace expiry — floor to 1s", -2 * stuckGracePeriod, time.Second},
		{"anchor now — full grace remaining", 0, stuckGracePeriod},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agFollowUpDelay(referenceTime.Add(tt.anchorOffset), referenceTime)
			if got != tt.want {
				t.Errorf("agFollowUpDelay(offset=%v) = %v, want %v", tt.anchorOffset, got, tt.want)
			}
		})
	}
}

// stuckAG builds an ArtifactGenerator in the fluxcd/pkg#934 stall signature:
// Inventory populated, ObservedSourcesDigest set, Ready=Unknown on the current
// generation, LastTransitionTime old enough to clear the grace period. Used by
// the maybeRecoverArtifactGenerator tests below.
func stuckAG(t *testing.T, annotations map[string]string, readyTransition time.Time) *sourcewatcherv1beta1.ArtifactGenerator {
	t.Helper()
	return &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "example",
			Namespace:   "cozy-system",
			Generation:  1,
			Annotations: annotations,
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
			Inventory: []sourcewatcherv1beta1.ExternalArtifactReference{
				{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
			},
			ObservedSourcesDigest: "sha256:0e7",
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionUnknown,
				Reason:             "Progressing",
				Message:            "Reconciliation in progress",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.NewTime(readyTransition),
			}},
		},
	}
}

// TestMaybeRecoverArtifactGenerator_ForceBranch pins the first-time-stuck path:
// no prior tracking annotations, so decideRecovery returns Force, we patch
// Ready=False on the AG's status, update tracking annotations, and set the
// PackageSource to Unknown/AwaitingSourceWatcherRecovery with a RequeueAfter
// equal to backoffFor(1).
func TestMaybeRecoverArtifactGenerator_ForceBranch(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
	}
	ag := stuckAG(t, nil, referenceTime.Add(-2*time.Minute))
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	ready := meta.FindStatusCondition(ag.Status.Conditions, "Ready")
	res, err := r.maybeRecoverArtifactGenerator(context.Background(), ps, ag, ready, referenceTime)
	if err != nil {
		t.Fatalf("maybeRecoverArtifactGenerator: %v", err)
	}
	if res.RequeueAfter != backoffFor(1) {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, backoffFor(1))
	}

	// AG status.Ready must be False in apiserver.
	persistedAG := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ag), persistedAG); err != nil {
		t.Fatalf("Get AG: %v", err)
	}
	agReady := meta.FindStatusCondition(persistedAG.Status.Conditions, "Ready")
	if agReady == nil || agReady.Status != metav1.ConditionFalse {
		t.Errorf("AG Ready in apiserver = %+v, want False", agReady)
	}
	if persistedAG.Annotations[annotationRecoveryAttempts] != "1" {
		t.Errorf("AG %s annotation = %q, want 1", annotationRecoveryAttempts, persistedAG.Annotations[annotationRecoveryAttempts])
	}

	// PackageSource must be Unknown/AwaitingSourceWatcherRecovery in apiserver.
	persistedPS := &cozyv1alpha1.PackageSource{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ps), persistedPS); err != nil {
		t.Fatalf("Get PS: %v", err)
	}
	psReady := meta.FindStatusCondition(persistedPS.Status.Conditions, "Ready")
	if psReady == nil || psReady.Status != metav1.ConditionUnknown || psReady.Reason != reasonAwaitingRecovery {
		t.Errorf("PS Ready = %+v, want Unknown/%s", psReady, reasonAwaitingRecovery)
	}
}

// TestMaybeRecoverArtifactGenerator_WaitBranch pins the in-backoff-window path:
// prior attempt exists, backoff not yet elapsed, so decideRecovery returns
// Wait and we do NOT touch the AG's status/annotations — only the PackageSource
// gets Unknown/AwaitingSourceWatcherRecovery and RequeueAfter set to the
// remaining backoff.
func TestMaybeRecoverArtifactGenerator_WaitBranch(t *testing.T) {
	priorForceAt := referenceTime.Add(-10 * time.Second) // 20s remaining on initialBackoff=30s
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
	}
	ag := stuckAG(t, map[string]string{
		annotationRecoveryAttempts: "1",
		annotationLastRecoveryAt:   priorForceAt.UTC().Format(time.RFC3339Nano),
	}, referenceTime.Add(-2*time.Minute))
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	agWriteCount := 0
	watched := interceptor.NewClient(c, interceptor.Funcs{
		Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*sourcewatcherv1beta1.ArtifactGenerator); ok {
				agWriteCount++
			}
			return cli.Patch(ctx, obj, patch, opts...)
		},
		SubResourcePatch: func(ctx context.Context, cli client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if _, ok := obj.(*sourcewatcherv1beta1.ArtifactGenerator); ok {
				agWriteCount++
			}
			return cli.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	})
	r := &PackageSourceReconciler{Client: watched, Scheme: testScheme(t)}

	ready := meta.FindStatusCondition(ag.Status.Conditions, "Ready")
	res, err := r.maybeRecoverArtifactGenerator(context.Background(), ps, ag, ready, referenceTime)
	if err != nil {
		t.Fatalf("maybeRecoverArtifactGenerator: %v", err)
	}
	if res.RequeueAfter != 20*time.Second {
		t.Errorf("RequeueAfter = %v, want 20s", res.RequeueAfter)
	}
	if agWriteCount != 0 {
		t.Errorf("wait branch wrote %d times to AG, want 0", agWriteCount)
	}
	persistedPS := &cozyv1alpha1.PackageSource{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ps), persistedPS); err != nil {
		t.Fatalf("Get PS: %v", err)
	}
	psReady := meta.FindStatusCondition(persistedPS.Status.Conditions, "Ready")
	if psReady == nil || psReady.Reason != reasonAwaitingRecovery {
		t.Errorf("PS Ready = %+v, want reason %s", psReady, reasonAwaitingRecovery)
	}
}

// TestMaybeRecoverArtifactGenerator_GiveUpBranch pins the exhaustion path:
// attempts equals maxRecoveryAttempts, so we surface the failure as
// PackageSource Ready=False/SourceWatcherStalled and schedule no follow-up
// reconcile (an operator must intervene). The AG is left untouched.
func TestMaybeRecoverArtifactGenerator_GiveUpBranch(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
	}
	priorForceAt := referenceTime.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	ag := stuckAG(t, map[string]string{
		annotationRecoveryAttempts: strconv.Itoa(maxRecoveryAttempts),
		annotationLastRecoveryAt:   priorForceAt,
	}, referenceTime.Add(-time.Hour)) // last transition BEFORE lastRecoveryAt so reset-counter doesn't fire
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	ready := meta.FindStatusCondition(ag.Status.Conditions, "Ready")
	res, err := r.maybeRecoverArtifactGenerator(context.Background(), ps, ag, ready, referenceTime)
	if err != nil {
		t.Fatalf("maybeRecoverArtifactGenerator: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 (give-up path stops rescheduling)", res.RequeueAfter)
	}
	persistedPS := &cozyv1alpha1.PackageSource{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ps), persistedPS); err != nil {
		t.Fatalf("Get PS: %v", err)
	}
	psReady := meta.FindStatusCondition(persistedPS.Status.Conditions, "Ready")
	if psReady == nil || psReady.Status != metav1.ConditionFalse || psReady.Reason != reasonSourceWatcherBad {
		t.Errorf("PS Ready = %+v, want False/%s", psReady, reasonSourceWatcherBad)
	}
}

// TestMaybeRecoverArtifactGenerator_DoesNotResetCounterOnFreshTransition pins
// the fix for lexfrei's B3: source-watcher writes Ready=Unknown (Progressing)
// as it takes the drifted branch after our force, and that transition's
// LastTransitionTime is always newer than lastRecoveryAt. A reset-counter
// heuristic that keys on that comparison would restart the budget on every
// Progressing write, making the SourceWatcherStalled give-up unreachable
// under the very race this driver targets. Assertion: with attempts already
// at maxRecoveryAttempts, we go straight to GiveUp even when Ready has just
// been touched — the natural clearRecoveryTracking on the not-stuck path is
// the only counter reset, and the awaitSourceWatcherResponse path in
// updateStatus holds tracking through Progressing.
func TestMaybeRecoverArtifactGenerator_DoesNotResetCounterOnFreshTransition(t *testing.T) {
	priorForceAt := referenceTime.Add(-time.Hour)
	freshTransitionAt := referenceTime.Add(-2 * time.Minute) // AFTER priorForceAt — must NOT reset
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
	}
	ag := stuckAG(t, map[string]string{
		annotationRecoveryAttempts: strconv.Itoa(maxRecoveryAttempts),
		annotationLastRecoveryAt:   priorForceAt.UTC().Format(time.RFC3339Nano),
	}, freshTransitionAt)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	ready := meta.FindStatusCondition(ag.Status.Conditions, "Ready")
	res, err := r.maybeRecoverArtifactGenerator(context.Background(), ps, ag, ready, referenceTime)
	if err != nil {
		t.Fatalf("maybeRecoverArtifactGenerator: %v", err)
	}
	// Give-up path returns no RequeueAfter (operator must intervene).
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 — give-up should not schedule a follow-up", res.RequeueAfter)
	}
	persistedPS := &cozyv1alpha1.PackageSource{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ps), persistedPS); err != nil {
		t.Fatalf("Get PS: %v", err)
	}
	psReady := meta.FindStatusCondition(persistedPS.Status.Conditions, "Ready")
	if psReady == nil || psReady.Status != metav1.ConditionFalse || psReady.Reason != reasonSourceWatcherBad {
		t.Errorf("PS Ready = %+v, want False/%s — reset would have taken the Force branch instead", psReady, reasonSourceWatcherBad)
	}
}

// TestUpdateStatus_OwnMarkerRoutesToAwait pins lexfrei's B2 fix: when the AG's
// Ready condition carries our own reasonRecoveryForced marker (our previous
// forceArtifactGeneratorDrift write reflecting back through the Owns() watch),
// updateStatus must (a) NOT clear the recovery-tracking annotations we just
// wrote, and (b) NOT copy the synthetic Ready=False through to the
// PackageSource. It routes to awaitSourceWatcherResponse and keeps the PS in
// AwaitingSourceWatcherRecovery.
func TestUpdateStatus_OwnMarkerRoutesToAwait(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Name: "src", Kind: "OCIRepository", Namespace: "cozy-system"},
		},
	}
	forceAt := referenceTime.Add(-5 * time.Second)
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "cozy-system", Generation: 1,
			CreationTimestamp: metav1.NewTime(referenceTime.Add(-time.Hour)),
			Annotations: map[string]string{
				annotationRecoveryAttempts: "1",
				annotationLastRecoveryAt:   forceAt.UTC().Format(time.RFC3339Nano),
			},
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
			Inventory: []sourcewatcherv1beta1.ExternalArtifactReference{
				{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
			},
			ObservedSourcesDigest: "sha256:0e7",
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             reasonRecoveryForced,
				Message:            "cozystack-operator forced drift",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.NewTime(forceAt),
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	agWrites := 0
	watched := interceptor.NewClient(c, interceptor.Funcs{
		Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*sourcewatcherv1beta1.ArtifactGenerator); ok {
				agWrites++
			}
			return cli.Patch(ctx, obj, patch, opts...)
		},
	})
	r := &PackageSourceReconciler{Client: watched, Scheme: testScheme(t)}

	res, err := r.updateStatus(context.Background(), ps, referenceTime)
	if err != nil {
		t.Fatalf("updateStatus: %v", err)
	}
	// The routing goes through maybeRecoverArtifactGenerator's Wait branch:
	// decideRecovery(attempts=1, lastRecoveryAt=referenceTime-5s, now=referenceTime)
	// → elapsed 5s, backoffFor(1)=30s → wait 25s. Exact equality catches
	// silent backoff-schedule regressions.
	if res.RequeueAfter != 25*time.Second {
		t.Errorf("RequeueAfter = %v, want 25s (backoffFor(1)=30s minus 5s elapsed)", res.RequeueAfter)
	}
	if agWrites != 0 {
		t.Errorf("clearRecoveryTracking wrote to AG %d times — tracking must be held while our own marker is visible", agWrites)
	}
	// AG's recovery-tracking annotations must still be intact.
	persistedAG := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ag), persistedAG); err != nil {
		t.Fatalf("Get AG: %v", err)
	}
	if got := persistedAG.Annotations[annotationRecoveryAttempts]; got != "1" {
		t.Errorf("AG %s = %q after own-marker reconcile, want 1 (held)", annotationRecoveryAttempts, got)
	}
	// PS must be Unknown/AwaitingSourceWatcherRecovery — NOT False/SourceWatcherRecoveryForced.
	persistedPS := &cozyv1alpha1.PackageSource{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ps), persistedPS); err != nil {
		t.Fatalf("Get PS: %v", err)
	}
	psReady := meta.FindStatusCondition(persistedPS.Status.Conditions, "Ready")
	if psReady == nil || psReady.Status != metav1.ConditionUnknown || psReady.Reason != reasonAwaitingRecovery {
		t.Errorf("PS Ready = %+v, want Unknown/%s — synthetic marker must not leak to PS status", psReady, reasonAwaitingRecovery)
	}
}

// TestUpdateStatus_ProgressingDuringRecoveryRoutesToAwait pins lexfrei's B2
// fix for the second Owns()-re-entrancy path: source-watcher writes
// Ready=Unknown/Progressing after taking the drifted branch and before
// finalising the rebuild. That transition arrives via the Owns() watch, and
// if we cleared tracking on it the attempts counter would never accumulate.
// Assertion: with tracking annotations present (attempts>0) and Ready=Unknown
// visible, we hold tracking and route to awaitSourceWatcherResponse.
func TestUpdateStatus_ProgressingDuringRecoveryRoutesToAwait(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Name: "src", Kind: "OCIRepository", Namespace: "cozy-system"},
		},
	}
	forceAt := referenceTime.Add(-10 * time.Second)
	progressingAt := referenceTime.Add(-3 * time.Second) // Newer than forceAt
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "cozy-system", Generation: 1,
			CreationTimestamp: metav1.NewTime(referenceTime.Add(-time.Hour)),
			Annotations: map[string]string{
				annotationRecoveryAttempts: "2",
				annotationLastRecoveryAt:   forceAt.UTC().Format(time.RFC3339Nano),
			},
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
			Inventory: []sourcewatcherv1beta1.ExternalArtifactReference{
				{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
			},
			ObservedSourcesDigest: "sha256:0e7",
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionUnknown,
				Reason:             "Progressing",
				Message:            "Reconciliation in progress",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.NewTime(progressingAt),
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	agWrites := 0
	watched := interceptor.NewClient(c, interceptor.Funcs{
		Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*sourcewatcherv1beta1.ArtifactGenerator); ok {
				agWrites++
			}
			return cli.Patch(ctx, obj, patch, opts...)
		},
	})
	r := &PackageSourceReconciler{Client: watched, Scheme: testScheme(t)}

	if _, err := r.updateStatus(context.Background(), ps, referenceTime); err != nil {
		t.Fatalf("updateStatus: %v", err)
	}
	if agWrites != 0 {
		t.Errorf("tracking cleared during recovery Progressing — attempts=2 wrote to AG %d times, want 0", agWrites)
	}
	persistedAG := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ag), persistedAG); err != nil {
		t.Fatalf("Get AG: %v", err)
	}
	if got := persistedAG.Annotations[annotationRecoveryAttempts]; got != "2" {
		t.Errorf("AG %s = %q, want 2 (held through Progressing)", annotationRecoveryAttempts, got)
	}
}

// TestUpdateStatus_ReadyTrueClearsTracking pins the canonical reset: once
// source-watcher writes Ready=True (recovery succeeded), the not-stuck path
// clears the recovery-tracking annotations and copies the real condition
// through. This is the only counter reset that remains after B3.
func TestUpdateStatus_ReadyTrueClearsTracking(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Name: "src", Kind: "OCIRepository", Namespace: "cozy-system"},
		},
	}
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "cozy-system", Generation: 1,
			CreationTimestamp: metav1.NewTime(referenceTime.Add(-time.Hour)),
			Annotations: map[string]string{
				annotationRecoveryAttempts: "3",
				annotationLastRecoveryAt:   referenceTime.Add(-time.Minute).UTC().Format(time.RFC3339Nano),
			},
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
			Inventory: []sourcewatcherv1beta1.ExternalArtifactReference{
				{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
			},
			ObservedSourcesDigest: "sha256:0e7",
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				Reason:             "Succeeded",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.NewTime(referenceTime.Add(-time.Second)),
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	if _, err := r.updateStatus(context.Background(), ps, referenceTime); err != nil {
		t.Fatalf("updateStatus: %v", err)
	}
	persistedAG := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ag), persistedAG); err != nil {
		t.Fatalf("Get AG: %v", err)
	}
	if _, ok := persistedAG.Annotations[annotationRecoveryAttempts]; ok {
		t.Errorf("recovery-attempts annotation still present after Ready=True: %v", persistedAG.Annotations)
	}
	persistedPS := &cozyv1alpha1.PackageSource{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ps), persistedPS); err != nil {
		t.Fatalf("Get PS: %v", err)
	}
	psReady := meta.FindStatusCondition(persistedPS.Status.Conditions, "Ready")
	if psReady == nil || psReady.Status != metav1.ConditionTrue {
		t.Errorf("PS Ready = %+v, want True (copied through from AG)", psReady)
	}
}

// TestUpdateStatus_OwnMarkerStale_TriggersRetry regression-tests the
// source-watcher-unresponsive path that an earlier iteration of this driver
// tripped on: after we force-drift, source-watcher never enqueues (network
// partition, missing predicate, pod dead), and our previous force's marker
// keeps reflecting back via Owns(). If updateStatus routed that into a
// perpetual "await" helper the recovery would tight-loop forever and
// SourceWatcherStalled would be unreachable. Asserted here: past
// backoffFor(attempts), the state machine issues another force (bumps
// attempts, re-stamps requestedAt) rather than idling.
func TestUpdateStatus_OwnMarkerStale_TriggersRetry(t *testing.T) {
	// attempts=1, lastRecoveryAt=referenceTime-60s. backoffFor(1)=30s, so
	// 60s > 30s → decideRecovery must Force again.
	forceAt := referenceTime.Add(-60 * time.Second)
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Name: "src", Kind: "OCIRepository", Namespace: "cozy-system"},
		},
	}
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "cozy-system", Generation: 1,
			CreationTimestamp: metav1.NewTime(referenceTime.Add(-time.Hour)),
			Annotations: map[string]string{
				annotationRecoveryAttempts: "1",
				annotationLastRecoveryAt:   forceAt.UTC().Format(time.RFC3339Nano),
				annotationFluxRequestedAt:  forceAt.UTC().Format(time.RFC3339Nano),
			},
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
			Inventory: []sourcewatcherv1beta1.ExternalArtifactReference{
				{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
			},
			ObservedSourcesDigest: "sha256:0e7",
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             reasonRecoveryForced,
				Message:            "cozystack-operator forced drift (source-watcher never responded)",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.NewTime(forceAt),
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	if _, err := r.updateStatus(context.Background(), ps, referenceTime); err != nil {
		t.Fatalf("updateStatus: %v", err)
	}
	// attempts must have incremented from 1 → 2.
	persistedAG := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ag), persistedAG); err != nil {
		t.Fatalf("Get AG: %v", err)
	}
	if got := persistedAG.Annotations[annotationRecoveryAttempts]; got != "2" {
		t.Errorf("AG %s = %q, want 2 — stale own-marker must escalate through Force, not idle", annotationRecoveryAttempts, got)
	}
	// requestedAt must have been re-stamped to a fresh timestamp so
	// source-watcher's next reconcile-request predicate fires.
	if got := persistedAG.Annotations[annotationFluxRequestedAt]; got == forceAt.UTC().Format(time.RFC3339Nano) {
		t.Errorf("AG %s = %q — must be re-stamped on retry", annotationFluxRequestedAt, got)
	}
}

// TestUpdateStatus_OwnMarkerAttemptsExhausted_GivesUp regression-tests the
// endpoint of the retry loop: with attempts=maxRecoveryAttempts and our own
// marker still reflecting back (source-watcher never reacted through the
// whole budget), updateStatus must surface Ready=False /
// SourceWatcherStalled on the PackageSource instead of looping forever.
func TestUpdateStatus_OwnMarkerAttemptsExhausted_GivesUp(t *testing.T) {
	forceAt := referenceTime.Add(-10 * time.Minute) // well past any backoff
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "cozy-system", Generation: 1},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Name: "src", Kind: "OCIRepository", Namespace: "cozy-system"},
		},
	}
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "cozy-system", Generation: 1,
			CreationTimestamp: metav1.NewTime(referenceTime.Add(-time.Hour)),
			Annotations: map[string]string{
				annotationRecoveryAttempts: strconv.Itoa(maxRecoveryAttempts),
				annotationLastRecoveryAt:   forceAt.UTC().Format(time.RFC3339Nano),
			},
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{
			Inventory: []sourcewatcherv1beta1.ExternalArtifactReference{
				{Name: "one", Namespace: "cozy-system", Digest: "sha256:aaa"},
			},
			ObservedSourcesDigest: "sha256:0e7",
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             reasonRecoveryForced,
				ObservedGeneration: 1,
				LastTransitionTime: metav1.NewTime(forceAt),
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ps, ag).WithObjects(ps, ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}

	res, err := r.updateStatus(context.Background(), ps, referenceTime)
	if err != nil {
		t.Fatalf("updateStatus: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v on give-up, want 0 (operator must intervene)", res.RequeueAfter)
	}
	persistedPS := &cozyv1alpha1.PackageSource{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ps), persistedPS); err != nil {
		t.Fatalf("Get PS: %v", err)
	}
	psReady := meta.FindStatusCondition(persistedPS.Status.Conditions, "Ready")
	if psReady == nil || psReady.Status != metav1.ConditionFalse || psReady.Reason != reasonSourceWatcherBad {
		t.Errorf("PS Ready = %+v, want False/%s — unresponsive source-watcher must eventually surface as stalled", psReady, reasonSourceWatcherBad)
	}
}
