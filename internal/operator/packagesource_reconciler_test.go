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

// TestCloneAnnotations pins the "shallow copy, nil in / nil out" contract that
// the rollback path in bumpArtifactGeneratorRequeue / clearRequeueTracking
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
	t.Run("populated in, deep copy out", func(t *testing.T) {
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

// TestBumpArtifactGeneratorRequeue_Success asserts that on a successful Patch:
//   - the three annotations land on the caller's `ag` (so re-reads of the same
//     pointer see the persisted state),
//   - the same three annotations land in the apiserver.
//
// This locks in the mutation contract documented on the function.
func TestBumpArtifactGeneratorRequeue_Success(t *testing.T) {
	ag := newAG(nil)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(ag).Build()
	r := &PackageSourceReconciler{Client: c, Scheme: testScheme(t)}
	now := referenceTime

	if err := r.bumpArtifactGeneratorRequeue(context.Background(), ag, now, 2); err != nil {
		t.Fatalf("bumpArtifactGeneratorRequeue: %v", err)
	}

	// In-memory ag reflects the write.
	if ag.Annotations == nil {
		t.Fatal("ag.Annotations is nil after successful bump")
	}
	if got := ag.Annotations[annotationRequeueAttempts]; got != "2" {
		t.Errorf("ag.Annotations[%s] = %q, want 2", annotationRequeueAttempts, got)
	}
	if got := ag.Annotations[annotationFluxRequestedAt]; got == "" {
		t.Errorf("ag.Annotations[%s] not set", annotationFluxRequestedAt)
	}
	if got := ag.Annotations[annotationLastRequeueAt]; got == "" {
		t.Errorf("ag.Annotations[%s] not set", annotationLastRequeueAt)
	}

	// apiserver reflects the write.
	persisted := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ag), persisted); err != nil {
		t.Fatalf("Get after bump: %v", err)
	}
	for _, k := range []string{annotationFluxRequestedAt, annotationRequeueAttempts, annotationLastRequeueAt} {
		if _, ok := persisted.Annotations[k]; !ok {
			t.Errorf("persisted AG missing annotation %s", k)
		}
	}
}

// TestBumpArtifactGeneratorRequeue_PatchFailure_Rollback drives the failure
// branch by wrapping the fake client with an interceptor that errors on Patch,
// and asserts the caller's `ag.Annotations` is restored to its pre-call value
// (nil in this test) — the whole point of introducing the rollback.
func TestBumpArtifactGeneratorRequeue_PatchFailure_Rollback(t *testing.T) {
	ag := newAG(nil)
	baseClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(ag).Build()
	failing := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return errors.New("simulated Patch failure")
		},
	})
	r := &PackageSourceReconciler{Client: failing, Scheme: testScheme(t)}

	err := r.bumpArtifactGeneratorRequeue(context.Background(), ag, referenceTime, 3)
	if err == nil {
		t.Fatal("bumpArtifactGeneratorRequeue succeeded, want error")
	}
	if ag.Annotations != nil {
		t.Errorf("ag.Annotations = %v after failed Patch, want nil (rolled back)", ag.Annotations)
	}
}

// TestBumpArtifactGeneratorRequeue_PatchFailure_RollbackPreservesUnrelated
// ensures the rollback restores the caller's pre-existing annotations rather
// than nuking them along with our writes.
func TestBumpArtifactGeneratorRequeue_PatchFailure_RollbackPreservesUnrelated(t *testing.T) {
	ag := newAG(map[string]string{"unrelated": "value"})
	baseClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(ag).Build()
	failing := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return errors.New("simulated Patch failure")
		},
	})
	r := &PackageSourceReconciler{Client: failing, Scheme: testScheme(t)}

	if err := r.bumpArtifactGeneratorRequeue(context.Background(), ag, referenceTime, 1); err == nil {
		t.Fatal("bump succeeded, want error")
	}
	if ag.Annotations["unrelated"] != "value" {
		t.Errorf("unrelated annotation lost after rollback: %v", ag.Annotations)
	}
	if _, ok := ag.Annotations[annotationRequeueAttempts]; ok {
		t.Errorf("tracking annotation leaked after rollback: %v", ag.Annotations)
	}
}

// TestClearRequeueTracking_Noop asserts that if the AG has no tracking
// annotations, we don't issue a Patch at all — otherwise every healthy
// reconcile would generate a no-op write.
func TestClearRequeueTracking_Noop(t *testing.T) {
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

	if err := r.clearRequeueTracking(context.Background(), ag); err != nil {
		t.Fatalf("clearRequeueTracking: %v", err)
	}
	if patchCount != 0 {
		t.Errorf("clearRequeueTracking issued %d Patch calls, want 0", patchCount)
	}
}

// TestClearRequeueTracking_PatchFailure_Rollback drives the failure branch and
// asserts the tracking annotations are restored on the caller's `ag`.
func TestClearRequeueTracking_PatchFailure_Rollback(t *testing.T) {
	original := map[string]string{
		annotationRequeueAttempts: "3",
		annotationLastRequeueAt:   referenceTime.UTC().Format(time.RFC3339Nano),
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

	if err := r.clearRequeueTracking(context.Background(), ag); err == nil {
		t.Fatal("clearRequeueTracking succeeded, want error")
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
	ag := newAG(map[string]string{annotationRequeueAttempts: "2"})

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
