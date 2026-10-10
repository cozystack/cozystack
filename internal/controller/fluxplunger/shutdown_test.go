// SPDX-License-Identifier: Apache-2.0

package fluxplunger

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// countingReader counts List calls and fails the first failLists of them.
type countingReader struct {
	client.Reader
	lists, failLists int
}

func (r *countingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.lists++
	if r.lists <= r.failLists {
		return errors.New("etcdserver: leader changed")
	}
	return r.Reader.List(ctx, list, opts...)
}

type stubManager struct {
	client  client.Client
	reader  client.Reader
	elected chan struct{}
}

func (m stubManager) Start(context.Context) error { return nil }
func (m stubManager) GetClient() client.Client    { return m.client }
func (m stubManager) GetAPIReader() client.Reader { return m.reader }
func (m stubManager) Elected() <-chan struct{}    { return m.elected }

// electedChan is the Elected channel of a manager that became leader.
func electedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// A flux-plunger that never became leader stops while the leader goes on; its
// drain would write next to the leader.
func TestRunManager_DoesNotDrainWithoutHavingLed(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fencedLiveRelease()).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	reader := &countingReader{Reader: c}
	if err := RunManager(context.Background(), stubManager{client: c, reader: reader, elected: make(chan struct{})}, 10*time.Second); err != nil {
		t.Fatalf("RunManager: %v", err)
	}
	if reader.lists != 0 {
		t.Fatal("a flux-plunger that never led must not drain")
	}
	hr := &helmv2.HelmRelease{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(fencedLiveRelease()), hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		t.Fatal("the leader's fence must be left to the leader")
	}
}

func fencedLiveRelease() *helmv2.HelmRelease {
	return &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Namespace: testNS, Name: testName, Finalizers: []string{fenceFinalizer},
	}}
}

// After the manager stops its informer cache is frozen, so the drain must read
// through the manager's API reader; a drain reading the cache would act on
// fences already gone.
func TestRunManager_DrainsThroughTheManagersAPIReader(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fencedLiveRelease()).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	reader := &countingReader{Reader: c}
	if err := RunManager(context.Background(), stubManager{client: c, reader: reader, elected: electedChan()}, 10*time.Second); err != nil {
		t.Fatalf("RunManager: %v", err)
	}
	if reader.lists == 0 {
		t.Fatal("the drain did not read through the API reader")
	}
	hr := &helmv2.HelmRelease{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(fencedLiveRelease()), hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
		t.Fatalf("the drain must release the fence, finalizers %v", hr.Finalizers)
	}
}

func TestDrainFences_RetriesAFailedListing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fencedLiveRelease()).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	reader := &countingReader{Reader: c, failLists: 2}
	r := &FluxPlunger{Client: c, APIReader: reader}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	left, err := r.DrainFences(ctx)
	if err != nil || left != 0 {
		t.Fatalf("a listing that fails and then succeeds must not end the drain: left=%d err=%v lists=%d", left, err, reader.lists)
	}
	if reader.lists < 3 {
		t.Fatalf("want the listing retried, lists=%d", reader.lists)
	}
}

func TestRunThenDrain_DrainsAfterTheManagerStopsWithItsOwnDeadline(t *testing.T) {
	stopped, drained := false, false
	signal, cancel := context.WithCancel(context.Background())
	run := func(ctx context.Context) error {
		cancel() // the stop signal
		<-ctx.Done()
		stopped = true
		return nil
	}
	drain := func(ctx context.Context) (int, error) {
		drained = true
		if !stopped {
			t.Error("drain ran before the manager stopped")
		}
		// The signal context is cancelled by now; the drain must get time of its own.
		if err := ctx.Err(); err != nil {
			t.Errorf("drain context was already done: %v", err)
		}
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > time.Minute || time.Until(dl) < 50*time.Second {
			t.Errorf("drain deadline %v, want about a minute from now", dl)
		}
		return 0, nil
	}

	if err := RunThenDrain(signal, run, drain, time.Minute); err != nil {
		t.Fatalf("RunThenDrain: %v", err)
	}
	if !drained {
		t.Fatal("drain did not run")
	}
}

// The next flux-plunger may become leader only once the old one has stopped its
// manager and finished draining, counted from the old one's last Lease renewal.
func TestApplyShutdownTimings_TheLeaseOutlastsTheStopAndTheDrain(t *testing.T) {
	for _, drain := range []time.Duration{2 * time.Second, 60 * time.Second, 110 * time.Second} {
		opts := ctrl.Options{}
		ApplyShutdownTimings(&opts, drain)
		if opts.LeaderElectionReleaseOnCancel {
			t.Fatalf("drain %v: a Lease released on stop lets the next flux-plunger lead during the drain", drain)
		}
		if opts.GracefulShutdownTimeout == nil || opts.LeaseDuration == nil || opts.RenewDeadline == nil || opts.RetryPeriod == nil {
			t.Fatalf("drain %v: timings left unset: %+v", drain, opts)
		}
		stop, lease, renew, retry := *opts.GracefulShutdownTimeout, *opts.LeaseDuration, *opts.RenewDeadline, *opts.RetryPeriod
		if stop <= 0 {
			t.Fatalf("drain %v: the manager's stop must be bounded, got %v", drain, stop)
		}
		// A leader whose renewals fail keeps acting until RenewDeadline has
		// passed since its last renewal, so that counts too.
		if lease <= retry+renew+stop+drain {
			t.Fatalf("drain %v: lease %v does not outlast a renewal %v ago, a renew deadline of %v, a stop of %v and the drain", drain, lease, retry, renew, stop)
		}
		if !(renew < lease && time.Duration(float64(retry)*1.2) < renew) {
			t.Fatalf("drain %v: client-go needs lease > renew > 1.2 * retry, got %v %v %v", drain, lease, renew, retry)
		}
	}
}

// controller-runtime reports a stop that overran GracefulShutdownTimeout as an
// error from Start. A reconcile slow to notice its cancelled context must not
// cost the drain the fence exists for; the exit stays non-zero.
func TestRunThenDrain_DrainsAfterAStopThatOverranItsGracePeriod(t *testing.T) {
	stopErr := fmt.Errorf("failed waiting for all runnables to end within grace period of 10s: %w", context.DeadlineExceeded)
	drained := false
	err := RunThenDrain(context.Background(),
		func(context.Context) error { return stopErr },
		func(context.Context) (int, error) { drained = true; return 0, nil },
		time.Minute)
	if !drained {
		t.Fatal("a stop that overran its grace period must still drain")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the overrun must still be reported, got %v", err)
	}
}

// The drain's Events cannot go through the manager's recorder, which is stopped
// before Start returns; they are written directly.
func TestRunManager_DrainEventsReachTheAPI(t *testing.T) {
	now := metav1.Now()
	hr := fencedLiveRelease()
	hr.Spec.Suspend = true
	hr.DeletionTimestamp = &now
	hr.Finalizers = append(hr.Finalizers, "example.com/keep")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hr).
		WithInterceptorFuncs(interceptor.Funcs{Patch: ssaModelApply}).Build()
	if err := RunManager(context.Background(), stubManager{client: c, reader: c, elected: electedChan()}, 5*time.Second); err != nil {
		t.Fatalf("RunManager: %v", err)
	}
	events := &corev1.EventList{}
	if err := c.List(context.Background(), events); err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range events.Items {
		if e.Reason == "TerminatingWhileSuspended" && e.InvolvedObject.Name == hr.Name && e.InvolvedObject.Namespace == hr.Namespace {
			return
		}
	}
	t.Fatalf("the drain's event did not reach the API: %+v", events.Items)
}

func TestRunThenDrain_DoesNotDrainWhenTheManagerFails(t *testing.T) {
	boom := errors.New("boom")
	drained := false
	err := RunThenDrain(context.Background(),
		func(context.Context) error { return boom },
		func(context.Context) (int, error) { drained = true; return 0, nil },
		time.Minute)
	if !errors.Is(err, boom) {
		t.Fatalf("want the manager's error, got %v", err)
	}
	if drained {
		t.Fatal("a manager that failed must not be followed by a drain")
	}
}

func TestRunThenDrain_AnIncompleteDrainIsNotAFailure(t *testing.T) {
	err := RunThenDrain(context.Background(),
		func(context.Context) error { return nil },
		func(context.Context) (int, error) { return 2, context.DeadlineExceeded },
		time.Minute)
	if err != nil {
		t.Fatalf("fences left for the next flux-plunger or the pre-delete hook are not an error: %v", err)
	}
}

// A stop cancels every reconcile, so an unsuspend cut short by it is not the
// failure the Warning reports: the drain lifts that suspension next.
func TestRecordUnsuspendFailed_IgnoresACancelledStop(t *testing.T) {
	rec := record.NewFakeRecorder(4)
	r := &FluxPlunger{Recorder: rec}
	r.recordUnsuspendFailed(fencedLiveRelease(), fmt.Errorf("apply: %w", context.Canceled))
	select {
	case e := <-rec.Events:
		t.Fatalf("a cancelled stop must not raise %q", e)
	default:
	}
	r.recordUnsuspendFailed(fencedLiveRelease(), errors.New("forbidden"))
	select {
	case <-rec.Events:
	default:
		t.Fatal("a real failure must still raise the Warning")
	}
}
