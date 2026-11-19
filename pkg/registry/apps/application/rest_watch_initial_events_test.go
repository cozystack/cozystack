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

package application

import (
	"context"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	"github.com/cozystack/cozystack/pkg/registry/registrytest"
)

func newWatchTestREST(t *testing.T) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	return NewREST(fc, fc, &config.Resource{
		Application: config.ApplicationConfig{Kind: "Redis", Singular: "redis", Plural: "redises"},
		Release:     config.ReleaseConfig{Prefix: "redis-"},
	})
}

func redisRelease(name string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-" + name,
			Namespace: "tenant-a",
			Labels: map[string]string{
				ApplicationKindLabel:  "Redis",
				ApplicationGroupLabel: appsv1alpha1.GroupName,
			},
		},
	}
}

func collectWatchEvents(w watch.Interface, n int, timeout time.Duration) []watch.Event {
	var out []watch.Event
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case ev, ok := <-w.ResultChan():
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
	return out
}

// driveCreateAndUpdate creates a release behind the watch and then touches it,
// because fake.Client.Watch doesn't replay existing objects as ADDED.
func driveCreateAndUpdate(ctx context.Context, t *testing.T, r *REST) {
	t.Helper()
	hr := redisRelease("cache")
	if err := r.c.Create(ctx, hr); err != nil {
		t.Fatalf("create HelmRelease: %v", err)
	}
	hr.Annotations = map[string]string{"touched": "1"}
	if err := r.c.Update(ctx, hr); err != nil {
		t.Fatalf("update HelmRelease: %v", err)
	}
}

func TestWatch_SendInitialEvents_EmitsInitialEventsEndBookmark(t *testing.T) {
	r := newWatchTestREST(t)
	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), "tenant-a"))
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternalversion.ListOptions{SendInitialEvents: &sendInitialEvents, AllowWatchBookmarks: true})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()
	driveCreateAndUpdate(ctx, t, r)

	evs := collectWatchEvents(w, 3, 2*time.Second)
	if len(evs) < 3 {
		t.Fatalf("expected at least 3 events (Added, Bookmark, Modified), got %d: %+v", len(evs), evs)
	}
	if evs[0].Type != watch.Added {
		t.Fatalf("event[0]: expected Added, got %s", evs[0].Type)
	}
	if evs[1].Type != watch.Bookmark {
		t.Fatalf("event[1]: expected Bookmark, got %s", evs[1].Type)
	}
	bookmark, ok := evs[1].Object.(*appsv1alpha1.Application)
	if !ok {
		t.Fatalf("event[1]: expected *Application, got %T", evs[1].Object)
	}
	if got := bookmark.Annotations[metav1.InitialEventsAnnotationKey]; got != "true" {
		t.Fatalf("event[1]: expected annotation %s=true, got %q", metav1.InitialEventsAnnotationKey, got)
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
		opts metainternalversion.ListOptions
		want bool
	}{
		{"plain watch defaulted by the apiserver", metainternalversion.ListOptions{SendInitialEvents: &yes}, false},
		{"watch list", metainternalversion.ListOptions{SendInitialEvents: &yes, AllowWatchBookmarks: true}, true},
		{"bookmarks only", metainternalversion.ListOptions{AllowWatchBookmarks: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWatchTestREST(t)
			spy := &registrytest.RawRecordingWatch{WithWatch: r.w}
			r.w = spy
			ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), "tenant-a"))
			defer cancel()

			w, err := r.Watch(ctx, &tc.opts)
			if err != nil {
				t.Fatalf("Watch returned error: %v", err)
			}
			defer w.Stop()

			raw := spy.RawFor(&helmv2.HelmReleaseList{})
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
	r := newWatchTestREST(t)
	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), "tenant-a"))
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternalversion.ListOptions{SendInitialEvents: &sendInitialEvents})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()
	driveCreateAndUpdate(ctx, t, r)

	evs := collectWatchEvents(w, 3, 2*time.Second)
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
