// SPDX-License-Identifier: Apache-2.0

package tenantsecret

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/registry/registrytest"
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

// TestWatch_SendInitialEvents_EmitsInitialEventsEndBookmark asserts the
// WatchList contract for TenantSecret: ADDED events, then a bookmark annotated
// with k8s.io/initial-events-end, then live events.
//
// fake.Client.Watch doesn't replay existing objects as ADDED, so the secret is
// created after the watch starts and then mutated to drive a live event.
func TestWatch_SendInitialEvents_EmitsInitialEventsEndBookmark(t *testing.T) {
	r := newTestREST(t)

	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), testNamespace))
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternal.ListOptions{SendInitialEvents: &sendInitialEvents, AllowWatchBookmarks: true})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	sec := makeTenantSecret("harbor-test-credentials", nil)
	if err := r.c.Create(ctx, sec); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	sec.Annotations = map[string]string{"touched": "1"}
	if err := r.c.Update(ctx, sec); err != nil {
		t.Fatalf("update secret: %v", err)
	}

	evs := collectEvents(t, w, 3, 2*time.Second)
	if len(evs) < 3 {
		t.Fatalf("expected at least 3 events (Added, Bookmark, Modified), got %d: %+v", len(evs), evs)
	}

	if evs[0].Type != watch.Added {
		t.Fatalf("event[0]: expected Added, got %s", evs[0].Type)
	}
	added, ok := evs[0].Object.(*corev1alpha1.TenantSecret)
	if !ok {
		t.Fatalf("event[0]: expected *TenantSecret, got %T", evs[0].Object)
	}
	if added.Name != "harbor-test-credentials" {
		t.Fatalf("event[0]: expected name harbor-test-credentials, got %q", added.Name)
	}

	if evs[1].Type != watch.Bookmark {
		t.Fatalf("event[1]: expected Bookmark, got %s", evs[1].Type)
	}
	bookmark, ok := evs[1].Object.(*corev1alpha1.TenantSecret)
	if !ok {
		t.Fatalf("event[1]: expected *TenantSecret, got %T", evs[1].Object)
	}
	if got := bookmark.Annotations[metav1.InitialEventsAnnotationKey]; got != "true" {
		t.Fatalf("event[1]: expected annotation %s=true, got %q", metav1.InitialEventsAnnotationKey, got)
	}
	if bookmark.ResourceVersion == "" {
		t.Fatal("event[1]: expected non-empty resourceVersion on bookmark")
	}

	if evs[2].Type != watch.Modified {
		t.Fatalf("event[2]: expected Modified after bookmark, got %s", evs[2].Type)
	}
}

// TestWatch_BackingBookmarksFollowTheClient pins that the backing watch asks for
// bookmarks only when the client did: every backing bookmark is forwarded, and
// the fake client never sends one, so the event stream alone cannot show it.
func TestWatch_BackingBookmarksFollowTheClient(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		name string
		opts metainternal.ListOptions
		want bool
	}{
		{"plain watch defaulted by the apiserver", metainternal.ListOptions{SendInitialEvents: &yes}, false},
		{"watch list", metainternal.ListOptions{SendInitialEvents: &yes, AllowWatchBookmarks: true}, true},
		{"bookmarks only", metainternal.ListOptions{AllowWatchBookmarks: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestREST(t)
			spy := &registrytest.RawRecordingWatch{WithWatch: r.w}
			r.w = spy
			ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), testNamespace))
			defer cancel()

			w, err := r.Watch(ctx, &tc.opts)
			if err != nil {
				t.Fatalf("Watch returned error: %v", err)
			}
			defer w.Stop()

			raw := spy.RawFor(&corev1.SecretList{})
			if raw == nil {
				t.Fatal("backing watch was called without raw options")
			}
			if raw.AllowWatchBookmarks != tc.want {
				t.Fatalf("backing AllowWatchBookmarks = %v, want %v", raw.AllowWatchBookmarks, tc.want)
			}
		})
	}
}

// TestWatch_PlainWatch_EmitsNoInitialEventsEndBookmark covers a watch with no
// resourceVersion: the apiserver defaults SendInitialEvents on for it but leaves
// bookmarks off, so the client never asked for the terminating bookmark.
func TestWatch_PlainWatch_EmitsNoInitialEventsEndBookmark(t *testing.T) {
	r := newTestREST(t)

	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), testNamespace))
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternal.ListOptions{SendInitialEvents: &sendInitialEvents})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	sec := makeTenantSecret("harbor-test-credentials", nil)
	if err := r.c.Create(ctx, sec); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	sec.Annotations = map[string]string{"touched": "1"}
	if err := r.c.Update(ctx, sec); err != nil {
		t.Fatalf("update secret: %v", err)
	}

	evs := collectEvents(t, w, 3, 2*time.Second)
	var types []watch.EventType
	for _, ev := range evs {
		types = append(types, ev.Type)
		if ev.Type == watch.Bookmark {
			t.Fatalf("unrequested bookmark on a plain watch: %+v", ev.Object)
		}
	}
	if len(types) != 2 || types[0] != watch.Added || types[1] != watch.Modified {
		t.Fatalf("expected [ADDED MODIFIED], got %v", types)
	}
}
