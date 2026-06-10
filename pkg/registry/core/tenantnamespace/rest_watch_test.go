// SPDX-License-Identifier: Apache-2.0

package tenantnamespace

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// newTestScheme returns a scheme with the types Watch tests need: core
// (namespaces drive the watch) and rbac (RoleBindings drive the access check).
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 to scheme: %v", err)
	}
	return scheme
}

func newTestREST(c client.Client, w client.WithWatch) *REST {
	return &REST{
		c: c,
		w: w,
		gvr: schema.GroupVersionResource{
			Group:    corev1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenantnamespaces",
		},
	}
}

// stubWithWatch backs REST.Watch with a test-owned FakeWatcher so tests can
// inject arbitrary backing events (bookmarks, foreign object types, chosen
// resourceVersions) that the controller-runtime fake client never produces.
// All other client calls fall through to the embedded fake client.
type stubWithWatch struct {
	client.WithWatch
	fw  *watch.FakeWatcher
	err error
}

// Watch ignores ListOptions entirely: selector propagation cannot be asserted
// through this stub, only through the real fake client.
func (s *stubWithWatch) Watch(_ context.Context, _ client.ObjectList, _ ...client.ListOption) (watch.Interface, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.fw, nil
}

func userRoleBinding(namespace, username string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: namespace},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: username, APIGroup: "rbac.authorization.k8s.io"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "test-role",
		},
	}
}

// requireTenantNamespaceEvent asserts the event has the given type and carries
// a *TenantNamespace with the given name.
func requireTenantNamespaceEvent(t *testing.T, ev watch.Event, evType watch.EventType, name string) *corev1alpha1.TenantNamespace {
	t.Helper()
	if ev.Type != evType {
		t.Fatalf("expected event type %s, got %s (%+v)", evType, ev.Type, ev)
	}
	tn, ok := ev.Object.(*corev1alpha1.TenantNamespace)
	if !ok {
		t.Fatalf("expected *TenantNamespace, got %T", ev.Object)
	}
	if tn.Name != name {
		t.Fatalf("expected namespace %q, got %q", name, tn.Name)
	}
	return tn
}

// waitForFakeWatcherStop polls until the filtering goroutine stops the
// backing watcher (via its deferred Stop) or the timeout fires.
func waitForFakeWatcherStop(t *testing.T, fw *watch.FakeWatcher, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !fw.IsStopped() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the goroutine to stop the backing watcher")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// requireResultChanClosed asserts the watch result channel closes within the
// timeout without delivering any further events.
func requireResultChanClosed(t *testing.T, w watch.Interface, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	select {
	case ev, open := <-w.ResultChan():
		if open {
			t.Fatalf("expected result channel to close, got event %+v", ev)
		}
	case <-deadline.C:
		t.Fatal("timed out waiting for the result channel to close")
	}
}

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
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	r := newTestREST(fc, fc)

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

// The dropped-event assertions below rely on the watch pipeline being FIFO: a
// dropped backing event always precedes the next delivered one, so when the
// first event a test receives is the later fixture, the earlier one was
// provably filtered rather than still in flight.

// TestWatch_FiltersUnauthorizedNamespaceEvents asserts the per-event access
// check: with a RoleBinding in only one of two tenant namespaces, ADD/MODIFY/
// DELETE events for the other never reach the client.
func TestWatch_FiltersUnauthorizedNamespaceEvents(t *testing.T) {
	scheme := newTestScheme(t)
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(userRoleBinding("tenant-allowed", "test-user")).
		Build()
	r := newTestREST(fc, fc)

	u := &user.DefaultInfo{Name: "test-user", Groups: []string{"system:authenticated"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	denied := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-denied"}}
	allowed := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-allowed"}}
	// Interleave so every authorized event is preceded by an unauthorized one.
	for _, ns := range []*corev1.Namespace{denied, allowed} {
		if err := fc.Create(ctx, ns); err != nil {
			t.Fatalf("create %s: %v", ns.Name, err)
		}
	}
	for _, ns := range []*corev1.Namespace{denied, allowed} {
		ns.Labels = map[string]string{"touched": "1"}
		if err := fc.Update(ctx, ns); err != nil {
			t.Fatalf("update %s: %v", ns.Name, err)
		}
	}
	for _, ns := range []*corev1.Namespace{denied, allowed} {
		if err := fc.Delete(ctx, ns); err != nil {
			t.Fatalf("delete %s: %v", ns.Name, err)
		}
	}

	evs := collectEvents(t, w, 3, 2*time.Second)
	if len(evs) != 3 {
		t.Fatalf("expected 3 events for tenant-allowed, got %d: %+v", len(evs), evs)
	}
	requireTenantNamespaceEvent(t, evs[0], watch.Added, "tenant-allowed")
	requireTenantNamespaceEvent(t, evs[1], watch.Modified, "tenant-allowed")
	requireTenantNamespaceEvent(t, evs[2], watch.Deleted, "tenant-allowed")
}

// TestWatch_PrivilegedGroups_SeeEverything asserts that members of the
// privileged groups receive events for every tenant namespace without any
// RoleBinding fixtures.
func TestWatch_PrivilegedGroups_SeeEverything(t *testing.T) {
	for _, group := range []string{"system:masters", "cozystack-cluster-admin"} {
		t.Run(group, func(t *testing.T) {
			fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
			r := newTestREST(fc, fc)

			u := &user.DefaultInfo{Name: "admin", Groups: []string{group}}
			ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
			defer cancel()

			w, err := r.Watch(ctx, &metainternal.ListOptions{})
			if err != nil {
				t.Fatalf("Watch returned error: %v", err)
			}
			defer w.Stop()

			for _, name := range []string{"tenant-a", "tenant-b"} {
				ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
				if err := fc.Create(ctx, ns); err != nil {
					t.Fatalf("create %s: %v", name, err)
				}
			}

			evs := collectEvents(t, w, 2, 2*time.Second)
			if len(evs) != 2 {
				t.Fatalf("expected 2 events, got %d: %+v", len(evs), evs)
			}
			requireTenantNamespaceEvent(t, evs[0], watch.Added, "tenant-a")
			requireTenantNamespaceEvent(t, evs[1], watch.Added, "tenant-b")
		})
	}
}

// TestWatch_DropsNonTenantNamespaces asserts that events for namespaces
// without the tenant- prefix are dropped even for privileged users.
func TestWatch_DropsNonTenantNamespaces(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	r := newTestREST(fc, fc)

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	for _, name := range []string{"kube-whatever", "tenant-foo"} {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := fc.Create(ctx, ns); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(evs), evs)
	}
	requireTenantNamespaceEvent(t, evs[0], watch.Added, "tenant-foo")
}

// TestWatch_DropsNonNamespaceObjects asserts that backing events carrying an
// object that is not a *corev1.Namespace are dropped.
func TestWatch_DropsNonNamespaceObjects(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	fw := watch.NewFake()
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, fw: fw})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	fw.Action(watch.Added, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "not-a-namespace"}})
	fw.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-foo"}})

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(evs), evs)
	}
	requireTenantNamespaceEvent(t, evs[0], watch.Added, "tenant-foo")
}

// TestWatch_ForwardsBackingBookmarks asserts that a bookmark from the backing
// watch is forwarded as a TenantNamespace bookmark carrying the backing
// resourceVersion. Without SendInitialEvents it must not be annotated as the
// initial-events-end marker.
func TestWatch_ForwardsBackingBookmarks(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	fw := watch.NewFake()
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, fw: fw})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	// A bookmark carrying a non-Namespace object must be dropped, not forwarded.
	fw.Action(watch.Bookmark, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "41"}})
	fw.Action(watch.Bookmark, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "42"}})

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(evs), evs)
	}
	if evs[0].Type != watch.Bookmark {
		t.Fatalf("expected Bookmark, got %s", evs[0].Type)
	}
	bookmark, ok := evs[0].Object.(*corev1alpha1.TenantNamespace)
	if !ok {
		t.Fatalf("expected *TenantNamespace, got %T", evs[0].Object)
	}
	if bookmark.Kind != "TenantNamespace" {
		t.Errorf("expected kind TenantNamespace, got %q", bookmark.Kind)
	}
	if bookmark.ResourceVersion != "42" {
		t.Errorf("expected resourceVersion 42, got %q", bookmark.ResourceVersion)
	}
	if _, ok := bookmark.Annotations[metav1.InitialEventsAnnotationKey]; ok {
		t.Errorf("bookmark must not carry %s without SendInitialEvents", metav1.InitialEventsAnnotationKey)
	}
}

// TestWatch_FieldSelectorFiltersEvents asserts metadata.name field selector
// filtering at the watch level.
func TestWatch_FieldSelectorFiltersEvents(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	r := newTestREST(fc, fc)

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", "tenant-foo"),
	})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	for _, name := range []string{"tenant-bar", "tenant-foo"} {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := fc.Create(ctx, ns); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(evs), evs)
	}
	requireTenantNamespaceEvent(t, evs[0], watch.Added, "tenant-foo")
}

// TestWatch_LabelSelectorFiltersEvents asserts label selector filtering at the
// watch level.
func TestWatch_LabelSelectorFiltersEvents(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	r := newTestREST(fc, fc)

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"team": "a"}),
	})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	unlabeled := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-bar"}}
	if err := fc.Create(ctx, unlabeled); err != nil {
		t.Fatalf("create tenant-bar: %v", err)
	}
	labeled := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "tenant-foo",
		Labels: map[string]string{"team": "a"},
	}}
	if err := fc.Create(ctx, labeled); err != nil {
		t.Fatalf("create tenant-foo: %v", err)
	}

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(evs), evs)
	}
	requireTenantNamespaceEvent(t, evs[0], watch.Added, "tenant-foo")
}

// TestWatch_MissingUser_ReturnsUnauthorized asserts Watch rejects a context
// without user info up front instead of failing per event.
func TestWatch_MissingUser_ReturnsUnauthorized(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	r := newTestREST(fc, fc)

	w, err := r.Watch(context.Background(), &metainternal.ListOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !apierrors.IsUnauthorized(err) {
		t.Fatalf("expected Unauthorized error, got %v", err)
	}
	if w != nil {
		t.Fatalf("expected nil watch on error, got %v", w)
	}
}

// TestWatch_AccessCheckError_SkipsEvent asserts that an error from the
// RoleBinding lookup drops the affected event without killing the watch.
func TestWatch_AccessCheckError_SkipsEvent(t *testing.T) {
	scheme := newTestScheme(t)
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(userRoleBinding("tenant-ok", "test-user")).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				lo := (&client.ListOptions{}).ApplyOptions(opts)
				if lo.Namespace == "tenant-broken" {
					return errors.New("rolebinding list failed")
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()
	r := newTestREST(fc, fc)

	u := &user.DefaultInfo{Name: "test-user", Groups: []string{"system:authenticated"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	for _, name := range []string{"tenant-broken", "tenant-ok"} {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := fc.Create(ctx, ns); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(evs), evs)
	}
	requireTenantNamespaceEvent(t, evs[0], watch.Added, "tenant-ok")
}

// TestWatch_SkipsAddedAtOrBelowStartingRV asserts that when the client
// supplies a resourceVersion, ADDED events at or below it are skipped as
// already known from the preceding List.
func TestWatch_SkipsAddedAtOrBelowStartingRV(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	fw := watch.NewFake()
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, fw: fw})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{ResourceVersion: "100"})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	fw.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-old", ResourceVersion: "100"}})
	fw.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-new", ResourceVersion: "101"}})

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(evs), evs)
	}
	requireTenantNamespaceEvent(t, evs[0], watch.Added, "tenant-new")
}

// TestWatch_OnClose_FlushesTerminatingBookmark asserts that when the backing
// watcher closes before any event was observed, a SendInitialEvents watch
// still emits the terminating bookmark at the starting resourceVersion.
func TestWatch_OnClose_FlushesTerminatingBookmark(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	fw := watch.NewFake()
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, fw: fw})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternal.ListOptions{
		ResourceVersion:   "5",
		SendInitialEvents: &sendInitialEvents,
	})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	fw.Stop()

	evs := collectEvents(t, w, 1, 2*time.Second)
	if len(evs) != 1 {
		t.Fatalf("expected 1 bookmark event, got %d: %+v", len(evs), evs)
	}
	// The terminating bookmark is the last event: the goroutine closes the
	// result channel on exit, signalling end-of-stream to the consumer.
	requireResultChanClosed(t, w, 2*time.Second)
	if evs[0].Type != watch.Bookmark {
		t.Fatalf("expected Bookmark, got %s", evs[0].Type)
	}
	bookmark, ok := evs[0].Object.(*corev1alpha1.TenantNamespace)
	if !ok {
		t.Fatalf("expected *TenantNamespace, got %T", evs[0].Object)
	}
	if got := bookmark.Annotations[metav1.InitialEventsAnnotationKey]; got != "true" {
		t.Errorf("expected annotation %s=true, got %q", metav1.InitialEventsAnnotationKey, got)
	}
	if bookmark.ResourceVersion != "5" {
		t.Errorf("expected resourceVersion 5, got %q", bookmark.ResourceVersion)
	}
}

// TestWatch_StopTerminatesGoroutine asserts that stopping the returned watch
// makes the filtering goroutine exit and stop the backing watcher. The result
// channel is intentionally never read, so the goroutine's send can only
// observe the stop signal.
func TestWatch_StopTerminatesGoroutine(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	fw := watch.NewFake()
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, fw: fw})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	w.Stop()
	// Push an event the goroutine must try (and fail) to deliver.
	fw.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-foo"}})

	waitForFakeWatcherStop(t, fw, 2*time.Second)
	requireResultChanClosed(t, w, 2*time.Second)
}

// TestWatch_ContextCancelTerminatesGoroutine asserts that cancelling the
// request context makes the filtering goroutine exit and stop the backing
// watcher. As above, the result channel is never read.
func TestWatch_ContextCancelTerminatesGoroutine(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	fw := watch.NewFake()
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, fw: fw})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	cancel()
	fw.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-foo"}})

	waitForFakeWatcherStop(t, fw, 2*time.Second)
	requireResultChanClosed(t, w, 2*time.Second)
}

// TestWatch_StoppedDuringBookmarkSend_TerminatesGoroutine asserts the early
// return when forwarding a backing bookmark fails because the watch was
// stopped: the result channel is never read, so the bookmark send can only
// observe the stop signal and the goroutine must exit.
func TestWatch_StoppedDuringBookmarkSend_TerminatesGoroutine(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	fw := watch.NewFake()
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, fw: fw})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	w.Stop()
	fw.Action(watch.Bookmark, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "7"}})

	waitForFakeWatcherStop(t, fw, 2*time.Second)
	requireResultChanClosed(t, w, 2*time.Second)
}

// TestWatch_StoppedDuringInitialEventsEndBookmarkSend_TerminatesGoroutine
// asserts the early return when delivering the initial-events-end bookmark
// ahead of the first live event fails because the watch was stopped.
func TestWatch_StoppedDuringInitialEventsEndBookmarkSend_TerminatesGoroutine(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	fw := watch.NewFake()
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, fw: fw})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx, cancel := context.WithCancel(request.WithUser(context.Background(), u))
	defer cancel()

	sendInitialEvents := true
	w, err := r.Watch(ctx, &metainternal.ListOptions{SendInitialEvents: &sendInitialEvents})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	w.Stop()
	// A live (non-ADDED) event makes the bookmarker emit the pending
	// initial-events-end bookmark first; its send must fail and exit.
	fw.Modify(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-foo", ResourceVersion: "8"}})

	waitForFakeWatcherStop(t, fw, 2*time.Second)
	requireResultChanClosed(t, w, 2*time.Second)
}

// TestWatch_BackingWatchError_Propagates asserts that a failure to start the
// backing watch is returned to the caller.
func TestWatch_BackingWatchError_Propagates(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	wantErr := errors.New("backing watch failed")
	r := newTestREST(fc, &stubWithWatch{WithWatch: fc, err: wantErr})

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx := request.WithUser(context.Background(), u)

	w, err := r.Watch(ctx, &metainternal.ListOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
	if w != nil {
		t.Fatalf("expected nil watch on error, got %v", w)
	}
}
