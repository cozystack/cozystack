// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package securitygroup

import (
	"context"
	"testing"
	"time"

	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"

	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
)

// collectEvents drains up to n events from the watch, or returns early if the
// channel closes or the timeout fires.
func collectEvents(t *testing.T, w watch.Interface, n int, timeout time.Duration) []watch.Event {
	t.Helper()
	out := make([]watch.Event, 0, n)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for len(out) < n {
		select {
		case ev, ok := <-w.ResultChan():
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline.C:
			return out
		}
	}
	return out
}

// fake.Client.Watch does not replay existing objects as ADDED, so every test
// here starts the watch first and then mutates the store to drive events.

func TestWatchFiltersUnmarkedPolicies(t *testing.T) {
	r := newTestREST(t)
	ctx, cancel := context.WithCancel(ctxNS())
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	// An unmarked platform policy must never surface; the marked one must.
	if err := r.c.Create(ctx, unmarkedPolicy("tenant-isolation")); err != nil {
		t.Fatalf("create unmarked: %v", err)
	}
	if err := r.c.Create(ctx, markedPolicy("sg-db")); err != nil {
		t.Fatalf("create marked: %v", err)
	}

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 event (the marked policy), got %d: %+v", len(evs), evs)
	}
	if evs[0].Type != watch.Added {
		t.Fatalf("expected Added, got %s", evs[0].Type)
	}
	sg, ok := evs[0].Object.(*sdnv1alpha1.SecurityGroup)
	if !ok {
		t.Fatalf("expected *SecurityGroup, got %T", evs[0].Object)
	}
	if sg.Name != "sg-db" {
		t.Fatalf("expected the marked policy sg-db, got %q", sg.Name)
	}
	if _, ok := sg.Labels[sgLabelKey]; ok {
		t.Fatalf("Watch leaked the marker label: %v", sg.Labels)
	}
}

func TestWatchDeletedEventPassesThrough(t *testing.T) {
	r := newTestREST(t)
	ctx, cancel := context.WithCancel(ctxNS())
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	if err := r.c.Create(ctx, markedPolicy("sg-db")); err != nil {
		t.Fatalf("create marked: %v", err)
	}
	if err := r.c.Delete(ctx, markedPolicy("sg-db")); err != nil {
		t.Fatalf("delete marked: %v", err)
	}

	evs := collectEvents(t, w, 2, 2*time.Second)
	if len(evs) < 2 {
		t.Fatalf("expected at least 2 events (Added, Deleted), got %d: %+v", len(evs), evs)
	}
	if evs[0].Type != watch.Added {
		t.Fatalf("event[0]: expected Added, got %s", evs[0].Type)
	}
	if evs[len(evs)-1].Type != watch.Deleted {
		t.Fatalf("last event: expected Deleted to pass through, got %s", evs[len(evs)-1].Type)
	}
}

func TestWatchSendInitialEventsEmitsBookmark(t *testing.T) {
	r := newTestREST(t)
	ctx, cancel := context.WithCancel(ctxNS())
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternal.ListOptions{SendInitialEvents: &sendInitialEvents})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	np := markedPolicy("sg-db")
	if err := r.c.Create(ctx, np); err != nil {
		t.Fatalf("create marked: %v", err)
	}
	np.Annotations = map[string]string{"touched": "1"}
	if err := r.c.Update(ctx, np); err != nil {
		t.Fatalf("update marked: %v", err)
	}

	evs := collectEvents(t, w, 3, 2*time.Second)
	if len(evs) < 3 {
		t.Fatalf("expected at least 3 events (Added, Bookmark, Modified), got %d: %+v", len(evs), evs)
	}
	if evs[0].Type != watch.Added {
		t.Fatalf("event[0]: expected Added, got %s", evs[0].Type)
	}
	if evs[1].Type != watch.Bookmark {
		t.Fatalf("event[1]: expected Bookmark, got %s", evs[1].Type)
	}
	bookmark, ok := evs[1].Object.(*sdnv1alpha1.SecurityGroup)
	if !ok {
		t.Fatalf("event[1]: expected *SecurityGroup, got %T", evs[1].Object)
	}
	if got := bookmark.Annotations[metav1.InitialEventsAnnotationKey]; got != "true" {
		t.Fatalf("event[1]: expected annotation %s=true, got %q", metav1.InitialEventsAnnotationKey, got)
	}
	if evs[2].Type != watch.Modified {
		t.Fatalf("event[2]: expected Modified after bookmark, got %s", evs[2].Type)
	}
}

func TestWatchFieldSelector(t *testing.T) {
	r := newTestREST(t)
	ctx, cancel := context.WithCancel(ctxNS())
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"metadata.name": "sg-a"}),
	})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	// sg-b is marked but does not match the field selector; only sg-a must surface.
	if err := r.c.Create(ctx, markedPolicy("sg-b")); err != nil {
		t.Fatalf("create sg-b: %v", err)
	}
	if err := r.c.Create(ctx, markedPolicy("sg-a")); err != nil {
		t.Fatalf("create sg-a: %v", err)
	}

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 event (sg-a), got %d: %+v", len(evs), evs)
	}
	sg, ok := evs[0].Object.(*sdnv1alpha1.SecurityGroup)
	if !ok || sg.Name != "sg-a" {
		t.Fatalf("field selector leaked a non-matching object: %+v", evs[0].Object)
	}
}

func TestWatchClusterWide(t *testing.T) {
	r := newTestREST(t)
	ctx, cancel := context.WithCancel(ctxAllNS())
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("cluster-wide Watch returned error: %v", err)
	}
	defer w.Stop()

	if err := r.c.Create(ctx, markedPolicyIn("sg-x", "tenant-x")); err != nil {
		t.Fatalf("create cross-namespace policy: %v", err)
	}

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 || evs[0].Type != watch.Added {
		t.Fatalf("cluster-wide watch did not stream the cross-namespace event: %+v", evs)
	}
}
