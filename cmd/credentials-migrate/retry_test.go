// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"syscall"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// readReset is what client-go returns when the connection drops mid-body.
var readReset = fmt.Errorf("unexpected error when reading response body. Please retry. Original error: %w", syscall.ECONNRESET)

func eventReasons(t *testing.T, m *migrator) map[string]int {
	t.Helper()
	evs := &corev1.EventList{}
	if err := m.c.List(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range evs.Items {
		out[e.Reason]++
	}
	return out
}

// switchedFixture is a seeded release on the converted chart whose source
// Secret reads fail `fails` times with a dropped connection.
func switchedFixture(t *testing.T, fails int) *migrator {
	t.Helper()
	transientDelay = 0
	left := fails
	funcs := &interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if _, ok := obj.(*helmv2.HelmRelease); ok && left > 0 && obj.GetAnnotations()["test/switched"] == "true" {
			left--
			return readReset
		}
		return c.Patch(ctx, obj, patch, opts...)
	}}
	hr := release(`{"users":{"web":{"password":"leftover"}}}`, "0.1.0", true)
	m := newMigratorWith(t, funcs, hr, sourceSecret(map[string]string{"web": "hunter2"}))
	ctx := context.Background()
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	cur := &helmv2.HelmRelease{}
	if err := m.c.Get(ctx, client.ObjectKeyFromObject(hr), cur); err != nil {
		t.Fatal(err)
	}
	cur.Status.History = helmv2.Snapshots{{Version: 2, ChartVersion: "0.2.0", Status: "deployed"}}
	if err := m.c.Status().Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	// Mark it so only finish's patches fail, not the seed's.
	cur.Annotations["test/switched"] = "true"
	if err := m.c.Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFinishRetriesATransientErrorWithoutASkipEvent(t *testing.T) {
	m := switchedFixture(t, 1)
	if err := m.finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := eventReasons(t, m); r[reasonSkipped] != 0 || r[reasonRetrying] != 0 {
		t.Fatalf("events = %v: a release converted on retry must not be announced as left out", r)
	}
	if st, _ := m.loadState(context.Background()); !st.Releases["tenant-foo/postgres-orders"].Done {
		t.Fatalf("state = %+v", st)
	}
}

func TestFinishAnnouncesAPersistentTransientErrorAsARetry(t *testing.T) {
	m := switchedFixture(t, 100)
	if err := m.finish(context.Background()); err == nil {
		t.Fatal("finish must fail so the Job retries")
	}
	r := eventReasons(t, m)
	if r[reasonSkipped] != 0 || r[reasonRetrying] != 1 {
		t.Fatalf("events = %v", r)
	}
	// Still recorded, so the incomplete alert fires if it never converts.
	if st, _ := m.loadState(context.Background()); st.Skipped["tenant-foo/postgres-orders"] == "" {
		t.Fatalf("state = %+v", st)
	}
}

func TestFinishStillSkipsAFinalError(t *testing.T) {
	transientDelay = 0
	hr := release(`{"users":{"web":{}}}`, "0.1.0", true)
	m := newMigrator(t, hr, sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Never switches: the wait times out, which no retry fixes.
	if err := m.finish(context.Background()); err == nil {
		t.Fatal("finish must fail")
	}
	if r := eventReasons(t, m); r[reasonSkipped] != 1 || r[reasonRetrying] != 0 {
		t.Fatalf("events = %v", r)
	}
}

func TestIsTransient(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{readReset, true},
		{fmt.Errorf("get x: %w", syscall.ECONNRESET), true},
		{fmt.Errorf("not Ready on a chart version other than %q", "0.1.0"), false},
		{fmt.Errorf("%w: users.x", errUnresolvable), false},
	} {
		if got := isTransient(c.err); got != c.want {
			t.Errorf("isTransient(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
