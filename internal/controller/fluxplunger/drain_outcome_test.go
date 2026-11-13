// SPDX-License-Identifier: Apache-2.0

package fluxplunger

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// logEntry is one line a captureSink saw; err is set for an error-level line.
type logEntry struct {
	msg string
	err error
	kv  []interface{}
}

// captureSink records every line logged through it, so a test can tell what an
// operator reading the pod's log would see, and at which level.
type captureSink struct {
	mu      *sync.Mutex
	entries *[]logEntry
	kv      []interface{}
}

func newCaptureLogger() (logr.Logger, *[]logEntry) {
	entries := &[]logEntry{}
	return logr.New(captureSink{mu: &sync.Mutex{}, entries: entries}), entries
}

func (s captureSink) Init(logr.RuntimeInfo)  {}
func (s captureSink) Enabled(level int) bool { return true }
func (s captureSink) Info(_ int, msg string, kv ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.entries = append(*s.entries, logEntry{msg: msg, kv: append(append([]interface{}{}, s.kv...), kv...)})
}
func (s captureSink) Error(err error, msg string, kv ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.entries = append(*s.entries, logEntry{msg: msg, err: err, kv: append(append([]interface{}{}, s.kv...), kv...)})
}
func (s captureSink) WithValues(kv ...interface{}) logr.LogSink {
	s.kv = append(append([]interface{}{}, s.kv...), kv...)
	return s
}
func (s captureSink) WithName(string) logr.LogSink { return s }

// Fences left behind reach the operator as an error-level line that names the
// releases, not an Info line an alert on errors never sees.
func TestRunThenDrain_FencesLeftBehindAreAnErrorNamingThem(t *testing.T) {
	logger, entries := newCaptureLogger()
	ctx := log.IntoContext(context.Background(), logger)
	RunThenDrain(ctx,
		func(context.Context) error { return nil },
		func(context.Context) (int, error) {
			return 1, errors.Join(context.DeadlineExceeded, errors.New("still fenced: tenant-root/vm-disk-0"))
		},
		time.Minute)
	for _, e := range *entries {
		if e.err != nil && strings.Contains(e.err.Error(), "tenant-root/vm-disk-0") {
			return
		}
	}
	t.Fatalf("no error-level line names the release still fenced: %+v", *entries)
}

// When no listing ever succeeded the drain does not know how many fences are
// held; the line must say so instead of reporting a count of -1.
func TestRunThenDrain_AnUnansweredListingIsNotACountOfMinusOne(t *testing.T) {
	logger, entries := newCaptureLogger()
	ctx := log.IntoContext(context.Background(), logger)
	RunThenDrain(ctx,
		func(context.Context) error { return nil },
		func(context.Context) (int, error) { return -1, context.DeadlineExceeded },
		time.Minute)
	found := false
	for _, e := range *entries {
		for i := 0; i+1 < len(e.kv); i += 2 {
			if v, ok := e.kv[i+1].(int); ok && v < 0 {
				t.Fatalf("a line reports %v=%d: %+v", e.kv[i], v, e)
			}
		}
		if e.err != nil && strings.Contains(e.msg, "no listing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no error-level line says no listing succeeded: %+v", *entries)
	}
}

// A drain that released everything says so once.
func TestRunThenDrain_ACompleteDrainSaysSo(t *testing.T) {
	logger, entries := newCaptureLogger()
	ctx := log.IntoContext(context.Background(), logger)
	RunThenDrain(ctx,
		func(context.Context) error { return nil },
		func(context.Context) (int, error) { return 0, nil },
		time.Minute)
	for _, e := range *entries {
		if e.err == nil && strings.Contains(e.msg, "Released every uninstall fence") {
			return
		}
	}
	t.Fatalf("a complete drain did not say so: %+v", *entries)
}

// A flux-plunger that never led drained nothing, so it must not claim it
// released every fence.
func TestRunManager_ANeverLedProcessDoesNotClaimItReleasedTheFences(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fencedLiveRelease()).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	logger, entries := newCaptureLogger()
	ctx := log.IntoContext(context.Background(), logger)
	if err := RunManager(ctx, stubManager{client: c, reader: c, elected: make(chan struct{})}, 5*time.Second); err != nil {
		t.Fatalf("RunManager: %v", err)
	}
	for _, e := range *entries {
		if strings.Contains(e.msg, "Released every uninstall fence") {
			t.Fatalf("a process that never led claims it released the fences: %+v", *entries)
		}
	}
}

func fencedLiveReleaseNamed(name string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Namespace: testNS, Name: name, Finalizers: []string{fenceFinalizer},
	}}
}

// patchFailingFor fails every patch of the named release and applies the rest.
func patchFailingFor(name string) func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
	return func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if obj.GetName() == name {
			return errors.New("the object has been modified; please apply your changes to the latest version")
		}
		return ssaModelApply(ctx, cl, obj, patch, opts...)
	}
}

// One release whose fence cannot be given up must not cost the others theirs.
func TestDrainFences_AFailingReleaseDoesNotStopTheOthers(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(fencedLiveReleaseNamed("a-fails"), fencedLiveReleaseNamed("b-ok")).
		WithInterceptorFuncs(interceptor.Funcs{Patch: patchFailingFor("a-fails")}).Build()
	r := &FluxPlunger{Client: c, APIReader: c}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	left, err := r.DrainFences(ctx)
	if err == nil || left != 1 {
		t.Fatalf("want the failing release left fenced and reported, left=%d err=%v", left, err)
	}
	if !strings.Contains(err.Error(), testNS+"/a-fails") {
		t.Errorf("the error does not name the release left fenced: %v", err)
	}
	hr := &helmv2.HelmRelease{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "b-ok"}, hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		t.Fatal("a release after a failing one kept its fence")
	}
}

// A drain cut short in the pass that released a fence must not name that
// release as still fenced: an operator would go looking at the wrong one.
func TestDrainFences_NamesOnlyTheReleasesStillFenced(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(fencedLiveReleaseNamed("a-fails"), fencedLiveReleaseNamed("b-ok")).
		WithInterceptorFuncs(interceptor.Funcs{Patch: patchFailingFor("a-fails")}).Build()
	r := &FluxPlunger{Client: c, APIReader: c}
	// Shorter than the pause between passes: the drain ends in its first pass.
	ctx, cancel := context.WithTimeout(context.Background(), drainPollInterval/2)
	defer cancel()
	left, err := r.DrainFences(ctx)
	if err == nil {
		t.Fatal("want the drain cut short")
	}
	if strings.Contains(err.Error(), "b-ok") {
		t.Errorf("the error names a release whose fence this pass released: %v", err)
	}
	if !strings.Contains(err.Error(), testNS+"/a-fails") || left != 1 {
		t.Errorf("want only a-fails named and counted, left=%d err=%v", left, err)
	}
}

// The drain polls; without a pause between passes it would list every
// HelmRelease in the cluster as fast as the apiserver answers.
func TestDrainFences_PausesBetweenPasses(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fencedLiveReleaseNamed("a-fails")).
		WithInterceptorFuncs(interceptor.Funcs{Patch: patchFailingFor("a-fails")}).Build()
	reader := &countingReader{Reader: c}
	r := &FluxPlunger{Client: c, APIReader: reader}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	_, _ = r.DrainFences(ctx)
	if reader.lists > 5 {
		t.Fatalf("%d listings in 2.5s; the drain must pause %v between passes", reader.lists, drainPollInterval)
	}
}

// Repeats of one Event on one object fold into a single counted Event, as the
// manager's recorder would fold them, instead of one object per drain pass.
func TestAPIEventRecorder_FoldsRepeats(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	rec := newAPIEventRecorder(context.Background(), c)
	hr := fencedLiveRelease()
	for i := 0; i < 3; i++ {
		rec.Event(hr, corev1.EventTypeWarning, "TerminatingWhileSuspended", "same message")
	}
	rec.Event(hr, corev1.EventTypeNormal, "SuspensionNotOwned", "another message")
	events := &corev1.EventList{}
	if err := c.List(context.Background(), events); err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events.Items) != 2 {
		t.Fatalf("want 2 Events (one folded, one distinct), got %d: %+v", len(events.Items), events.Items)
	}
	for _, e := range events.Items {
		want := int32(1)
		if e.Reason == "TerminatingWhileSuspended" {
			want = 3
		}
		if e.Count != want {
			t.Errorf("%s: count %d, want %d", e.Reason, e.Count, want)
		}
	}
}

// The chart's ClusterRole grants events create and patch, not update; a fold
// that updates the Event is refused in a real cluster and the count stays 1.
func TestAPIEventRecorder_FoldsWithTheVerbsTheChartGrants(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.Event); ok {
				return apierrors.NewForbidden(corev1.Resource("events"), obj.GetName(), errors.New("update is not granted"))
			}
			return cl.Update(ctx, obj, opts...)
		},
	}).Build()
	rec := newAPIEventRecorder(context.Background(), c)
	for i := 0; i < 3; i++ {
		rec.Event(fencedLiveRelease(), corev1.EventTypeWarning, "TerminatingWhileSuspended", "same message")
	}
	events := &corev1.EventList{}
	if err := c.List(context.Background(), events); err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events.Items) != 1 || events.Items[0].Count != 3 {
		t.Fatalf("want one Event counted 3 without the update verb, got %+v", events.Items)
	}
}

// Each write is capped well inside the shortest drain the chart allows (15s),
// so one slow write cannot take the rest of the drain with it. The bound is
// spelled out here rather than read from eventWriteTimeout, so raising the cap
// fails this test instead of moving it.
func TestAPIEventRecorder_CapsEachWrite(t *testing.T) {
	var got time.Time
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			got, _ = ctx.Deadline()
			return cl.Create(ctx, obj, opts...)
		},
	}).Build()
	parent, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	start := time.Now()
	newAPIEventRecorder(parent, c).Event(fencedLiveRelease(), corev1.EventTypeWarning, "TerminatingWhileSuspended", "m")
	if got.IsZero() || got.Sub(start) > 10*time.Second {
		t.Fatalf("event write deadline %v after start, want at most 10s", got.Sub(start))
	}
}

// An Event write must not outlive the drain: the pod is killed when the grace
// period ends, whatever is in flight.
func TestAPIEventRecorder_WritesInsideTheDrainsDeadline(t *testing.T) {
	var got time.Time
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			got, _ = ctx.Deadline()
			return cl.Create(ctx, obj, opts...)
		},
	}).Build()
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := parent.Deadline()
	newAPIEventRecorder(parent, c).Event(fencedLiveRelease(), corev1.EventTypeWarning, "TerminatingWhileSuspended", "m")
	if got.IsZero() || got.After(want) {
		t.Fatalf("event write deadline %v is not inside the drain's %v", got, want)
	}
}
