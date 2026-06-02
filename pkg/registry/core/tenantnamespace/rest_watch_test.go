// SPDX-License-Identifier: Apache-2.0

package tenantnamespace

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
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
// WatchList contract for TenantNamespace: ADDED events, then a bookmark
// annotated with k8s.io/initial-events-end, then live events.
//
// The user is in system:masters so the access check passes without RBAC
// fixtures. fake.Client.Watch doesn't replay existing objects as ADDED, so the
// namespace is created after the watch starts and then mutated to drive a live
// event.
func TestWatch_SendInitialEvents_EmitsInitialEventsEndBookmark(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &REST{
		c: fc,
		w: fc,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenantnamespaces",
		},
	}

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternal.ListOptions{SendInitialEvents: &sendInitialEvents})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-foo"}}
	if err := r.c.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	ns.Labels = map[string]string{"touched": "1"}
	if err := r.c.Update(ctx, ns); err != nil {
		t.Fatalf("update namespace: %v", err)
	}

	evs := collectEvents(t, w, 3, 2*time.Second)
	if len(evs) < 3 {
		t.Fatalf("expected at least 3 events (Added, Bookmark, Modified), got %d: %+v", len(evs), evs)
	}

	if evs[0].Type != watch.Added {
		t.Fatalf("event[0]: expected Added, got %s", evs[0].Type)
	}
	added, ok := evs[0].Object.(*corev1alpha1.TenantNamespace)
	if !ok {
		t.Fatalf("event[0]: expected *TenantNamespace, got %T", evs[0].Object)
	}
	if added.Name != "tenant-foo" {
		t.Fatalf("event[0]: expected name tenant-foo, got %q", added.Name)
	}

	if evs[1].Type != watch.Bookmark {
		t.Fatalf("event[1]: expected Bookmark, got %s", evs[1].Type)
	}
	bookmark, ok := evs[1].Object.(*corev1alpha1.TenantNamespace)
	if !ok {
		t.Fatalf("event[1]: expected *TenantNamespace, got %T", evs[1].Object)
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
