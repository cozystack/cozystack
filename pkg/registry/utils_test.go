/*
Copyright 2024 The Cozystack Authors.

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

package registry

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// newTestBookmarkObject returns a minimal object for the bookmarker to stamp.
func newTestBookmarkObject() runtime.Object {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "core.cozystack.io/v1alpha1", Kind: "TenantSecret"},
	}
}

// assertInitialEventsEnd verifies ev is a Bookmark carrying
// metav1.InitialEventsAnnotationKey=true and a non-empty resourceVersion.
func assertInitialEventsEnd(t *testing.T, ev watch.Event, wantRV string) {
	t.Helper()
	if ev.Type != watch.Bookmark {
		t.Fatalf("expected Bookmark event, got %s", ev.Type)
	}
	obj, ok := ev.Object.(*metav1.PartialObjectMetadata)
	if !ok {
		t.Fatalf("expected *PartialObjectMetadata, got %T", ev.Object)
	}
	if got := obj.Annotations[metav1.InitialEventsAnnotationKey]; got != "true" {
		t.Fatalf("expected annotation %s=true, got %q", metav1.InitialEventsAnnotationKey, got)
	}
	if obj.ResourceVersion == "" {
		t.Fatal("expected non-empty resourceVersion on terminating bookmark")
	}
	if wantRV != "" && obj.ResourceVersion != wantRV {
		t.Fatalf("expected resourceVersion %q, got %q", wantRV, obj.ResourceVersion)
	}
}

func TestInitialEventsBookmarker_Disabled(t *testing.T) {
	b := NewInitialEventsBookmarker(false, "", newTestBookmarkObject)

	if !b.Sent() {
		t.Fatal("disabled bookmarker should report Sent()==true")
	}
	if _, ok := b.BeforeLiveEvent(watch.Modified); ok {
		t.Fatal("disabled bookmarker should never need a before-live bookmark")
	}
	if _, ok := b.OnClose(); ok {
		t.Fatal("disabled bookmarker should never need an on-close bookmark")
	}
	// A backing bookmark is still forwarded, but never carries the marker.
	ev, end := b.OnBackingBookmark("7")
	if end {
		t.Fatal("disabled bookmarker should not mark a backing bookmark as initial-events-end")
	}
	if ev.Type != watch.Bookmark {
		t.Fatalf("expected forwarded Bookmark, got %s", ev.Type)
	}
	if obj := ev.Object.(*metav1.PartialObjectMetadata); obj.Annotations[metav1.InitialEventsAnnotationKey] != "" {
		t.Fatal("disabled bookmarker must not annotate forwarded bookmarks")
	}
}

func TestInitialEventsBookmarker_BackingBookmarkTerminatesStream(t *testing.T) {
	b := NewInitialEventsBookmarker(true, "", newTestBookmarkObject)
	b.Observe("12")

	ev, end := b.OnBackingBookmark("15")
	if !end {
		t.Fatal("first backing bookmark should terminate the initial stream")
	}
	assertInitialEventsEnd(t, ev, "15")
	if !b.Sent() {
		t.Fatal("bookmarker should report Sent()==true after terminating bookmark")
	}

	// Subsequent backing bookmarks are forwarded without the marker.
	ev2, end2 := b.OnBackingBookmark("20")
	if end2 {
		t.Fatal("second backing bookmark must not re-terminate the stream")
	}
	if obj := ev2.Object.(*metav1.PartialObjectMetadata); obj.Annotations[metav1.InitialEventsAnnotationKey] != "" {
		t.Fatal("subsequent bookmark must not carry the initial-events-end annotation")
	}
}

func TestInitialEventsBookmarker_BeforeLiveEvent(t *testing.T) {
	b := NewInitialEventsBookmarker(true, "", newTestBookmarkObject)
	b.Observe("9")

	// ADDED events are part of the initial stream and never trigger the marker.
	if _, ok := b.BeforeLiveEvent(watch.Added); ok {
		t.Fatal("ADDED event should not trigger the initial-events-end bookmark")
	}

	ev, ok := b.BeforeLiveEvent(watch.Modified)
	if !ok {
		t.Fatal("first non-ADDED event should trigger the initial-events-end bookmark")
	}
	assertInitialEventsEnd(t, ev, "9")

	// Once sent, later live events do not re-trigger it.
	if _, ok := b.BeforeLiveEvent(watch.Deleted); ok {
		t.Fatal("initial-events-end bookmark should only be emitted once")
	}
}

func TestInitialEventsBookmarker_OnClose(t *testing.T) {
	b := NewInitialEventsBookmarker(true, "", newTestBookmarkObject)
	b.Observe("42")

	ev, ok := b.OnClose()
	if !ok {
		t.Fatal("closing before the marker was sent should flush a terminating bookmark")
	}
	assertInitialEventsEnd(t, ev, "42")

	if _, ok := b.OnClose(); ok {
		t.Fatal("on-close bookmark must not be emitted twice")
	}
}

func TestInitialEventsBookmarker_ObserveIgnoresEmpty(t *testing.T) {
	b := NewInitialEventsBookmarker(true, "", newTestBookmarkObject)
	b.Observe("5")
	b.Observe("") // must not clobber the tracked version

	ev, _ := b.OnClose()
	assertInitialEventsEnd(t, ev, "5")
}

func TestInitialEventsBookmarker_SeedsStartingResourceVersion(t *testing.T) {
	// Watcher closes before any backing event is observed: the terminating
	// bookmark falls back to the client's requested resourceVersion rather than
	// an empty one.
	b := NewInitialEventsBookmarker(true, "100", newTestBookmarkObject)

	ev, ok := b.OnClose()
	if !ok {
		t.Fatal("expected a terminating bookmark on close")
	}
	assertInitialEventsEnd(t, ev, "100")
}

func TestInitialEventsBookmarker_ObserveOverridesStartingResourceVersion(t *testing.T) {
	// Once a backing event is observed, its resourceVersion supersedes the seed.
	b := NewInitialEventsBookmarker(true, "100", newTestBookmarkObject)
	b.Observe("150")

	ev, _ := b.OnClose()
	assertInitialEventsEnd(t, ev, "150")
}
