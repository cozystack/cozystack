// SPDX-License-Identifier: Apache-2.0

package tenantmodule

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

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const testNamespace = "tenant-foo"

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
// WatchList contract for TenantModule: ADDED events, then a bookmark annotated
// with k8s.io/initial-events-end, then live events.
//
// fake.Client.Watch doesn't replay existing objects as ADDED, so the HelmRelease
// is created after the watch starts and then mutated to drive a live event.
func TestWatch_SendInitialEvents_EmitsInitialEventsEndBookmark(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add helmv2 to scheme: %v", err)
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := NewREST(fc, fc)

	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), testNamespace))
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternalversion.ListOptions{SendInitialEvents: &sendInitialEvents})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "module-a",
			Namespace: testNamespace,
			Labels: map[string]string{
				TenantModuleLabelKey: TenantModuleLabelValue,
			},
		},
	}
	if err := r.c.Create(ctx, hr); err != nil {
		t.Fatalf("create HelmRelease: %v", err)
	}
	hr.Annotations = map[string]string{"touched": "1"}
	if err := r.c.Update(ctx, hr); err != nil {
		t.Fatalf("update HelmRelease: %v", err)
	}

	evs := collectEvents(t, w, 3, 2*time.Second)
	if len(evs) < 3 {
		t.Fatalf("expected at least 3 events (Added, Bookmark, Modified), got %d: %+v", len(evs), evs)
	}

	if evs[0].Type != watch.Added {
		t.Fatalf("event[0]: expected Added, got %s", evs[0].Type)
	}
	added, ok := evs[0].Object.(*corev1alpha1.TenantModule)
	if !ok {
		t.Fatalf("event[0]: expected *TenantModule, got %T", evs[0].Object)
	}
	if added.Name != "module-a" {
		t.Fatalf("event[0]: expected name module-a, got %q", added.Name)
	}

	if evs[1].Type != watch.Bookmark {
		t.Fatalf("event[1]: expected Bookmark, got %s", evs[1].Type)
	}
	bookmark, ok := evs[1].Object.(*corev1alpha1.TenantModule)
	if !ok {
		t.Fatalf("event[1]: expected *TenantModule, got %T", evs[1].Object)
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
