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
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// MaxResourceVersion returns the maximum resourceVersion from all items in a list.
// This is useful when the list's ResourceVersion is empty (e.g., from controller-runtime cache).
func MaxResourceVersion(list runtime.Object) (string, error) {
	var max uint64

	err := meta.EachListItem(list, func(obj runtime.Object) error {
		accessor, err := meta.Accessor(obj)
		if err != nil {
			return err
		}

		rvStr := accessor.GetResourceVersion()
		if rvStr == "" {
			return nil
		}

		rv, err := strconv.ParseUint(rvStr, 10, 64)
		if err != nil {
			return err
		}

		if rv > max {
			max = rv
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	return strconv.FormatUint(max, 10), nil
}

// InitialEventsBookmarker implements the WatchList initial-events-end contract
// for a Watch that translates events from a backing controller-runtime watcher.
// It annotates the first backing Bookmark as the terminating marker, falling
// back to emitting one before the first live event or when the watcher closes.
//
// Construct with NewInitialEventsBookmarker. Methods are not safe for concurrent
// use; call them from a single watch goroutine.
type InitialEventsBookmarker struct {
	// newBookmark returns an empty, TypeMeta-populated object of the translated
	// resource type; the bookmarker fills in the resourceVersion and annotation.
	newBookmark func() runtime.Object
	sent        bool
	lastRV      string
}

// NewInitialEventsBookmarker returns a bookmarker for a watch. When
// sendInitialEvents is false every method is a no-op. startingRV seeds the
// resourceVersion (the client's requested ResourceVersion) so the terminating
// bookmark carries a valid version even if the watcher closes before any
// backing event is observed.
func NewInitialEventsBookmarker(sendInitialEvents bool, startingRV string, newBookmark func() runtime.Object) *InitialEventsBookmarker {
	return &InitialEventsBookmarker{
		newBookmark: newBookmark,
		lastRV:      startingRV,
		sent:        !sendInitialEvents,
	}
}

// Sent reports whether the initial-events-end bookmark has been emitted (or was
// never required). Callers buffering live events use it to decide when to start
// delivering them.
func (b *InitialEventsBookmarker) Sent() bool { return b.sent }

// Observe records the resourceVersion of the latest backing event so the
// bookmark carries an up-to-date version. Empty versions are ignored.
func (b *InitialEventsBookmarker) Observe(resourceVersion string) {
	if resourceVersion != "" {
		b.lastRV = resourceVersion
	}
}

// bookmarkEvent builds a Bookmark at the tracked resourceVersion, annotated as
// the terminating marker when end is true.
func (b *InitialEventsBookmarker) bookmarkEvent(end bool) watch.Event {
	obj := b.newBookmark()
	if accessor, err := meta.Accessor(obj); err == nil {
		accessor.SetResourceVersion(b.lastRV)
		if end {
			accessor.SetAnnotations(map[string]string{metav1.InitialEventsAnnotationKey: "true"})
		}
	}
	return watch.Event{Type: watch.Bookmark, Object: obj}
}

// OnBackingBookmark forwards the backing watcher's Bookmark at rv. The first one
// after the initial events becomes the terminating marker; the returned bool
// reports whether this call emitted it, so callers can flush buffered events.
func (b *InitialEventsBookmarker) OnBackingBookmark(rv string) (event watch.Event, initialEventsEnd bool) {
	b.Observe(rv)
	if b.sent {
		return b.bookmarkEvent(false), false
	}
	b.sent = true
	return b.bookmarkEvent(true), true
}

// BeforeLiveEvent returns the terminating bookmark and true when a live
// (non-ADDED) event is about to be delivered and the marker is still pending.
func (b *InitialEventsBookmarker) BeforeLiveEvent(eventType watch.EventType) (event watch.Event, needed bool) {
	if b.sent || eventType == watch.Added {
		return watch.Event{}, false
	}
	b.sent = true
	return b.bookmarkEvent(true), true
}

// OnClose returns the terminating bookmark and true if the backing watcher
// closed before the marker was emitted.
func (b *InitialEventsBookmarker) OnClose() (event watch.Event, needed bool) {
	if b.sent {
		return watch.Event{}, false
	}
	b.sent = true
	return b.bookmarkEvent(true), true
}
