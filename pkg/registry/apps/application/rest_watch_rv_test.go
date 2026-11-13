package application

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// rvWatchFixture models the two clients REST uses: c reads from an informer
// cache and writes to the API server, w talks to the API server directly.
// The cache can be left behind the live release, and the HelmRelease and
// WorkloadMonitor watches are fake watchers the test drives.
type rvWatchFixture struct {
	rest  *REST
	cache client.Client
	live  client.WithWatch
	hrW   *watch.FakeWatcher
	wmW   *watch.FakeWatcher
	// liveReleaseGets counts HelmRelease reads that bypass the cache. The
	// watch goroutine and the test both update it; receiving an event from
	// the watch orders the watch's updates before the test's next access.
	liveReleaseGets int
}

var rvReleaseKey = client.ObjectKey{Namespace: "default", Name: "postgresql-mydb"}

func newRVWatchFixture(t *testing.T) *rvWatchFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	release := func() *helmv2.HelmRelease {
		return &helmv2.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rvReleaseKey.Name,
				Namespace: rvReleaseKey.Namespace,
				Labels: map[string]string{
					ApplicationKindLabel:  "PostgreSQL",
					ApplicationGroupLabel: appsv1alpha1.GroupName,
					ApplicationNameLabel:  "mydb",
				},
			},
			Spec: helmv2.HelmReleaseSpec{Values: &apiextensionsv1.JSON{Raw: []byte(`{"marker":"cached"}`)}},
		}
	}

	f := &rvWatchFixture{hrW: watch.NewFake(), wmW: watch.NewFake()}
	liveStore := fake.NewClientBuilder().WithScheme(scheme).WithObjects(release()).Build()
	f.live = interceptor.NewClient(liveStore, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*helmv2.HelmRelease); ok {
				f.liveReleaseGets++
			}
			return c.Get(ctx, key, obj, opts...)
		},
		Watch: func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) (watch.Interface, error) {
			if _, ok := list.(*cozyv1alpha1.WorkloadMonitorList); ok {
				return f.wmW, nil
			}
			return f.hrW, nil
		},
	})
	f.cache = interceptor.NewClient(fake.NewClientBuilder().WithScheme(scheme).WithObjects(release()).Build(), interceptor.Funcs{
		Delete: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			return liveStore.Delete(ctx, obj, opts...)
		},
	})
	f.rest = NewREST(f.cache, f.live, &config.Resource{
		Application: config.ApplicationConfig{Kind: "PostgreSQL", Plural: "postgresqls", Singular: "postgresql"},
		Release:     config.ReleaseConfig{Prefix: "postgresql-"},
	})
	return f
}

func (f *rvWatchFixture) liveRelease(t *testing.T) *helmv2.HelmRelease {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := f.live.Get(context.Background(), rvReleaseKey, hr); err != nil {
		t.Fatal(err)
	}
	return hr
}

// advanceLive writes a new spec to the live release only, leaving the cache
// one version behind, and returns the new live release.
func (f *rvWatchFixture) advanceLive(t *testing.T, marker string) *helmv2.HelmRelease {
	t.Helper()
	hr := f.liveRelease(t)
	before := hr.ResourceVersion
	hr.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(`{"marker":"` + marker + `"}`)}
	if err := f.live.Update(context.Background(), hr); err != nil {
		t.Fatal(err)
	}
	if hr.ResourceVersion == before {
		t.Fatal("fake client did not advance the release version")
	}
	return hr
}

func (f *rvWatchFixture) monitorEvent(rv string) {
	f.wmW.Modify(&cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "mon-1",
			Namespace:       "default",
			ResourceVersion: rv,
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: appsv1alpha1.GroupName,
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{Operational: new(true)},
	})
}

func startRVWatch(t *testing.T, f *rvWatchFixture) (context.Context, watch.Interface) {
	t.Helper()
	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), "default"))
	t.Cleanup(cancel)
	w, err := f.rest.Watch(ctx, &metainternalversion.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	return ctx, w
}

func nextApplication(t *testing.T, w watch.Interface) *appsv1alpha1.Application {
	t.Helper()
	select {
	case ev, ok := <-w.ResultChan():
		if !ok {
			t.Fatal("watch closed")
		}
		app, ok := ev.Object.(*appsv1alpha1.Application)
		if !ok {
			t.Fatalf("got %s %T, want an Application", ev.Type, ev.Object)
		}
		return app
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return nil
}

func specMarker(t *testing.T, app *appsv1alpha1.Application) string {
	t.Helper()
	var v struct {
		Marker string `json:"marker"`
	}
	if app.Spec == nil {
		t.Fatal("application has no spec")
	}
	if err := json.Unmarshal(app.Spec.Raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.Marker
}

func rvNumber(t *testing.T, rv string) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(rv, 10, 64)
	if err != nil {
		t.Fatalf("resourceVersion %q: %v", rv, err)
	}
	return n
}

// The spec in a monitor-triggered event must come from a release read no
// earlier than the monitor revision the event carries, or a precondition on
// that version would accept a write built on a stale spec.
func TestWatchMonitorEventReadsReleaseLive(t *testing.T) {
	f := newRVWatchFixture(t)
	_, w := startRVWatch(t, f)
	f.advanceLive(t, "live")

	f.monitorEvent("5000")
	app := nextApplication(t, w)
	if got := specMarker(t, app); got != "live" {
		t.Fatalf("spec marker %q, want the live release's", got)
	}
	if app.ResourceVersion != "5000" {
		t.Fatalf("resourceVersion %q, want the monitor's 5000", app.ResourceVersion)
	}
	if f.liveReleaseGets != 2 {
		t.Fatalf("%d live release reads, want the test's own plus one for the event", f.liveReleaseGets)
	}
}

// A release cached at or past the monitor revision already pairs its spec
// with its own version, so the event uses it without a live read: a watch
// replays every existing monitor as ADDED when it starts. With nothing sent
// for the object yet, the client may hold that release version, so the event
// carries the monitor's.
func TestWatchMonitorEventUsesCacheNotOlderThanMonitor(t *testing.T) {
	f := newRVWatchFixture(t)
	_, w := startRVWatch(t, f)
	cached := &helmv2.HelmRelease{}
	if err := f.cache.Get(context.Background(), rvReleaseKey, cached); err != nil {
		t.Fatal(err)
	}
	f.advanceLive(t, "live")
	gets := f.liveReleaseGets

	f.monitorEvent("1")
	app := nextApplication(t, w)
	if got := specMarker(t, app); got != "cached" {
		t.Fatalf("spec marker %q, want the cached release's", got)
	}
	if app.ResourceVersion != "1" {
		t.Fatalf("resourceVersion %q, want the monitor's 1 rather than the cached release's %q", app.ResourceVersion, cached.ResourceVersion)
	}
	if f.liveReleaseGets != gets {
		t.Fatalf("%d live release reads for a monitor older than the cached release, want none", f.liveReleaseGets-gets)
	}
}

// Shared informers deliver an update whose resourceVersion equals the stored
// one only to handlers due for a resync, so each monitor-only change must
// arrive with a resourceVersion different from the previous event's.
func TestWatchMonitorOnlyChangeAdvancesVersion(t *testing.T) {
	f := newRVWatchFixture(t)
	_, w := startRVWatch(t, f)
	live := f.advanceLive(t, "live")

	f.hrW.Modify(live.DeepCopy())
	prev := rvNumber(t, nextApplication(t, w).ResourceVersion)
	for _, rv := range []string{"5000", "5001"} {
		f.monitorEvent(rv)
		got := rvNumber(t, nextApplication(t, w).ResourceVersion)
		if got <= prev {
			t.Fatalf("monitor event resourceVersion %d, want above the previous event's %d", got, prev)
		}
		prev = got
	}
}

// A monitor event processed after the release event it predates must not
// repeat the resourceVersion the client already holds, or a shared informer
// hides it from its handlers. It falls back to the monitor's older version,
// and a delete with that version gets a conflict instead of acting on a spec
// the client may not have.
func TestWatchLateMonitorEventDoesNotRepeatVersion(t *testing.T) {
	f := newRVWatchFixture(t)
	ctx, w := startRVWatch(t, f)
	live := f.liveRelease(t)

	f.hrW.Modify(live.DeepCopy())
	if got := nextApplication(t, w).ResourceVersion; got != live.ResourceVersion {
		t.Fatalf("release event resourceVersion %q, want %q", got, live.ResourceVersion)
	}
	f.monitorEvent("1")
	rv := nextApplication(t, w).ResourceVersion
	if rv != "1" {
		t.Fatalf("late monitor event resourceVersion %q, want the monitor's 1 rather than the held %q", rv, live.ResourceVersion)
	}
	_, _, err := f.rest.Delete(ctx, "mydb", nil, &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
	if !apierrors.IsConflict(err) {
		t.Fatalf("delete with the monitor's older version: got %v, want Conflict", err)
	}
}

// A release the client already holds is skipped on a resumed watch, so the
// next monitor event must not repeat its version either.
func TestWatchLateMonitorEventAfterSkippedReplay(t *testing.T) {
	f := newRVWatchFixture(t)
	live := f.liveRelease(t)
	w := startRVWatchFrom(t, f, live.ResourceVersion)

	f.hrW.Add(live.DeepCopy())
	f.monitorEvent("1")
	if rv := nextApplication(t, w).ResourceVersion; rv != "1" {
		t.Fatalf("monitor event after the skipped replay has resourceVersion %q, want the monitor's 1 rather than the held %q", rv, live.ResourceVersion)
	}
}

// Once this watch has sent the object a monitor version, the release version
// is no longer what the client holds, and the event can carry it.
func TestWatchMonitorEventCarriesReleaseAfterFallback(t *testing.T) {
	f := newRVWatchFixture(t)
	_, w := startRVWatch(t, f)
	live := f.liveRelease(t)

	f.monitorEvent("1")
	if rv := nextApplication(t, w).ResourceVersion; rv != "1" {
		t.Fatalf("first monitor event resourceVersion %q, want the monitor's 1", rv)
	}
	f.monitorEvent("2")
	if rv := nextApplication(t, w).ResourceVersion; rv != live.ResourceVersion {
		t.Fatalf("second monitor event resourceVersion %q, want the release's %q", rv, live.ResourceVersion)
	}
}

// A release event in between makes the release version the held one again.
func TestWatchReleaseEventResetsHeldVersion(t *testing.T) {
	f := newRVWatchFixture(t)
	_, w := startRVWatch(t, f)
	live := f.liveRelease(t)

	f.monitorEvent("1")
	nextApplication(t, w)
	f.hrW.Modify(live.DeepCopy())
	if rv := nextApplication(t, w).ResourceVersion; rv != live.ResourceVersion {
		t.Fatalf("release event resourceVersion %q, want %q", rv, live.ResourceVersion)
	}
	f.monitorEvent("2")
	if rv := nextApplication(t, w).ResourceVersion; rv != "2" {
		t.Fatalf("monitor event after the release event has resourceVersion %q, want the monitor's 2 rather than the held %q", rv, live.ResourceVersion)
	}
}

func startRVWatchFrom(t *testing.T, f *rvWatchFixture, resourceVersion string) watch.Interface {
	t.Helper()
	ctx, cancel := context.WithCancel(request.WithNamespace(context.Background(), "default"))
	t.Cleanup(cancel)
	w, err := f.rest.Watch(ctx, &metainternalversion.ListOptions{ResourceVersion: resourceVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	return w
}

// On a resumed watch the replayed monitor can arrive before the replayed
// release, so nothing is known yet about the version the client holds.
func TestWatchResumedMonitorReplayBeforeRelease(t *testing.T) {
	f := newRVWatchFixture(t)
	live := f.liveRelease(t)
	w := startRVWatchFrom(t, f, live.ResourceVersion)

	f.monitorEvent("1")
	if rv := nextApplication(t, w).ResourceVersion; rv != "1" {
		t.Fatalf("replayed monitor event has resourceVersion %q, want the monitor's 1 rather than the possibly held %q", rv, live.ResourceVersion)
	}
}

// A fallback event steps the client's resume point below versions it holds,
// so a resumed client can hold a release version above the one it resumes
// from.
func TestWatchResumedBelowHeldReleaseVersion(t *testing.T) {
	f := newRVWatchFixture(t)
	live := f.liveRelease(t)
	if rvNumber(t, live.ResourceVersion) <= 500 {
		t.Fatalf("fixture release version %q must be above the resume point", live.ResourceVersion)
	}
	w := startRVWatchFrom(t, f, "500")

	f.monitorEvent("1")
	if rv := nextApplication(t, w).ResourceVersion; rv != "1" {
		t.Fatalf("replayed monitor event has resourceVersion %q, want the monitor's 1 rather than the possibly held %q", rv, live.ResourceVersion)
	}
}

func TestDeleteWithVersionFromMonitorEvent(t *testing.T) {
	f := newRVWatchFixture(t)
	ctx, w := startRVWatch(t, f)
	f.advanceLive(t, "live")

	f.monitorEvent("5000")
	rv := nextApplication(t, w).ResourceVersion
	_, _, err := f.rest.Delete(ctx, "mydb", nil, &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
	if err != nil {
		t.Fatalf("delete with resourceVersion %q from the watch event: %v", rv, err)
	}
	if err := f.live.Get(context.Background(), rvReleaseKey, &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
		t.Fatalf("release still present after delete: %v", err)
	}
}

func TestDeleteRejectsVersionOlderThanLive(t *testing.T) {
	f := newRVWatchFixture(t)
	cached := &helmv2.HelmRelease{}
	if err := f.cache.Get(context.Background(), rvReleaseKey, cached); err != nil {
		t.Fatal(err)
	}
	f.advanceLive(t, "live")

	ctx := request.WithNamespace(context.Background(), "default")
	rv := cached.ResourceVersion
	_, _, err := f.rest.Delete(ctx, "mydb", nil, &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
	if !apierrors.IsConflict(err) {
		t.Fatalf("delete with stale resourceVersion %q: got %v, want Conflict", rv, err)
	}
	if err := f.live.Get(context.Background(), rvReleaseKey, &helmv2.HelmRelease{}); err != nil {
		t.Fatalf("release gone after a rejected delete: %v", err)
	}
}

func TestCheckResourceVersion(t *testing.T) {
	r := newTestRESTWithSchemesFromClient(nil)
	for _, tc := range []struct {
		caller string
		check  func(error) bool
	}{
		{"100", func(err error) bool { return err == nil }},
		{"101", func(err error) bool { return err == nil }},
		{"99", apierrors.IsConflict},
		{"0", apierrors.IsConflict},
		{"", apierrors.IsBadRequest},
		{"abc", apierrors.IsBadRequest},
	} {
		if err := r.checkResourceVersion("mydb", tc.caller, "100"); !tc.check(err) {
			t.Errorf("caller resourceVersion %q against live 100: unexpected result %v", tc.caller, err)
		}
	}
}

func TestNewerResourceVersion(t *testing.T) {
	for _, tc := range []struct{ a, b, want string }{
		{"7", "5", "7"},
		{"5", "7", "7"},
		{"7", "7", "7"},
		{"x", "5", "5"},
		{"7", "y", "y"},
	} {
		if got := newerResourceVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("newerResourceVersion(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}
