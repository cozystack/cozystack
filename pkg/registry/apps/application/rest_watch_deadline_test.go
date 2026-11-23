package application

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
)

func expectLiveReadWatchClosed(t *testing.T, w watch.Interface) {
	t.Helper()
	select {
	case event, open := <-w.ResultChan():
		if open {
			t.Fatalf("unexpected %s event instead of watch closure", event.Type)
		}
	default:
		t.Fatal("watch remains open after the live-read deadline")
	}
}

func TestWatchHungLiveReadDeadlineClosesAndReconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newRVWatchFixture(t)
		parent, w := startRVWatch(t, f)
		cached := f.liveRelease(t)
		f.hrW.Modify(cached.DeepCopy())
		resume := nextApplication(t, w).ResourceVersion
		live := f.advanceLive(t, "before-timeout")
		readsBefore := f.liveReleaseGets
		entered := make(chan context.Context, 1)
		f.liveReleaseErrs = []error{errors.New("live read failed")}
		f.onLiveReleaseError = func(ctx context.Context) {
			entered <- ctx
			<-ctx.Done()
		}

		readStarted := time.Now()
		f.monitorEvent(live.ResourceVersion)
		synctest.Wait()
		var liveCtx context.Context
		select {
		case liveCtx = <-entered:
		default:
			t.Fatal("live read did not start")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		expectLiveReadWatchClosed(t, w)
		if !errors.Is(liveCtx.Err(), context.DeadlineExceeded) {
			t.Fatalf("live read ended with %v, want its own deadline", liveCtx.Err())
		}
		deadline, bounded := liveCtx.Deadline()
		if !bounded || deadline.Sub(readStarted) != time.Minute {
			t.Fatalf("live read budget %v, bounded=%t, want exactly one minute", deadline.Sub(readStarted), bounded)
		}
		if parent.Err() != nil {
			t.Fatalf("watch parent unexpectedly ended: %v", parent.Err())
		}
		if got := f.liveReleaseGets - readsBefore; got != 1 {
			t.Fatalf("live release reads %d, want one bounded hung read", got)
		}

		f.hrW, f.wmW = watch.NewFake(), watch.NewFake()
		fresh := f.advanceLive(t, "after-reconnect")
		reconnected := startRVWatchFrom(t, f, resume)
		f.monitorEvent(fresh.ResourceVersion)
		app := nextApplication(t, reconnected)
		if got := specMarker(t, app); got != "after-reconnect" {
			t.Fatalf("reconnected spec marker %q, want fresh live state", got)
		}
		if app.ResourceVersion != fresh.ResourceVersion {
			t.Fatalf("reconnected version %q, want %q", app.ResourceVersion, fresh.ResourceVersion)
		}
	})
}

func TestWatchLiveReadDeadlineIncludesRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newRVWatchFixture(t)
		_, w := startRVWatch(t, f)
		live := f.advanceLive(t, "live")
		readsBefore := f.liveReleaseGets
		f.liveReleaseErrs = []error{errors.New("temporary live read failure")}
		f.onLiveReleaseError = func(context.Context) {
			time.Sleep(45 * time.Second)
			f.liveReleaseHang = true
		}

		f.monitorEvent(live.ResourceVersion)
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		expectLiveReadWatchClosed(t, w)
		if got := f.liveReleaseGets - readsBefore; got != 2 {
			t.Fatalf("live release reads %d, want one slow failure and one hung retry", got)
		}
	})
}

func TestWatchCancellationEndsHungLiveReadBeforeDeadline(t *testing.T) {
	for _, mode := range []string{"stop", "context"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newRVWatchFixture(t)
				parent, cancel := context.WithCancel(request.WithNamespace(context.Background(), "default"))
				t.Cleanup(cancel)
				w, err := f.rest.Watch(parent, &metainternalversion.ListOptions{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(w.Stop)
				live := f.advanceLive(t, "live")
				readsBefore := f.liveReleaseGets
				entered := make(chan context.Context, 1)
				f.liveReleaseErrs = []error{errors.New("live read failed")}
				f.onLiveReleaseError = func(ctx context.Context) {
					entered <- ctx
					<-ctx.Done()
				}
				f.monitorEvent(live.ResourceVersion)
				synctest.Wait()
				liveCtx := <-entered
				before := time.Now()
				if mode == "stop" {
					w.Stop()
				} else {
					cancel()
				}
				synctest.Wait()
				expectLiveReadWatchClosed(t, w)
				if !errors.Is(liveCtx.Err(), context.Canceled) {
					t.Fatalf("live read ended with %v, want cancellation", liveCtx.Err())
				}
				if time.Since(before) != 0 {
					t.Fatal("cancellation waited for a timer")
				}
				if got := f.liveReleaseGets - readsBefore; got != 1 {
					t.Fatalf("live release reads %d, want exactly one", got)
				}
			})
		})
	}
}
