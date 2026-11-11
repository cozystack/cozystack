/*
Copyright 2025 The Cozystack Authors.

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

package proxmoxnetworkcontroller

import (
	"context"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
)

func TestTrackDeletedSubnets(t *testing.T) {
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		annotation *string
		gone       []string
		notBefore  time.Time
		want       string // annotation afterwards; "" means none
		newest     time.Time
	}{{
		name: "new names start now", gone: []string{"b", "a"},
		want: `{"a":"2026-10-07T08:00:00Z","b":"2026-10-07T08:00:00Z"}`, newest: now,
	}, {
		name: "tracked names keep their start, the newest one counts", annotation: ptr(`{"a":"2026-10-07T07:00:00Z"}`), gone: []string{"a", "b"},
		want: `{"a":"2026-10-07T07:00:00Z","b":"2026-10-07T08:00:00Z"}`, newest: now,
	}, {
		name: "names no longer gone are dropped", annotation: ptr(`{"a":"2026-10-07T07:00:00Z","b":"2026-10-07T07:30:00Z"}`), gone: []string{"a"},
		want: `{"a":"2026-10-07T07:00:00Z"}`, newest: now.Add(-time.Hour),
	}, {
		name: "nothing gone removes the annotation", annotation: ptr(`{"a":"2026-10-07T07:00:00Z"}`),
	}, {
		name: "an unreadable value starts afresh", annotation: ptr(`not json`), gone: []string{"a"},
		want: `{"a":"2026-10-07T08:00:00Z"}`, newest: now,
	}, {
		name: "an unreadable time starts afresh", annotation: ptr(`{"a":"yesterday"}`), gone: []string{"a"},
		want: `{"a":"2026-10-07T08:00:00Z"}`, newest: now,
	}, {
		name: "a time in the future starts now", annotation: ptr(`{"a":"2026-10-08T08:00:00Z"}`), gone: []string{"a"},
		want: `{"a":"2026-10-07T08:00:00Z"}`, newest: now,
	}, {
		name: "a time before notBefore starts now, one at or after it is kept", annotation: ptr(`{"a":"2026-10-07T07:00:00Z","b":"2026-10-07T07:30:00Z"}`),
		gone: []string{"a", "b"}, notBefore: time.Date(2026, 10, 7, 7, 30, 0, 500, time.UTC),
		want: `{"a":"2026-10-07T08:00:00Z","b":"2026-10-07T07:30:00Z"}`, newest: now,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			v := zoneVlan("subnet-a", 101, tenantA, "subnet-a")
			if tc.annotation != nil {
				v.Annotations = map[string]string{pxv1.AnnotationDeletedSubnets: *tc.annotation}
			}
			c := newClient(t, v)
			newest, err := trackDeletedSubnets(context.Background(), c, v, tc.gone, now, tc.notBefore)
			if err != nil {
				t.Fatal(err)
			}
			if !newest.Equal(tc.newest) {
				t.Fatalf("newest = %v, want %v", newest, tc.newest)
			}
			got := &Vlan{}
			if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, got); err != nil {
				t.Fatal(err)
			}
			a, ok := got.Annotations[pxv1.AnnotationDeletedSubnets]
			if a != tc.want || ok != (tc.want != "") {
				t.Fatalf("annotation = %q (present %v), want %q", a, ok, tc.want)
			}

			// The same pass a minute later changes nothing and writes nothing.
			before := got.DeepCopy()
			again, err := trackDeletedSubnets(context.Background(), c, got, tc.gone, now.Add(time.Minute), tc.notBefore)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.gone) > 0 && !again.Equal(tc.newest) {
				t.Fatalf("a second pass moved the newest timer to %v, want %v", again, tc.newest)
			}
			wantUnwritten(t, c, before)
		})
	}
}

func TestAdoptVlan(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		vlan        func() *Vlan
		wantWritten bool
	}{{
		name:        "a stale timer is dropped",
		vlan:        func() *Vlan { return timedVlan(`{"subnet-a":"2026-10-07T07:00:00Z"}`) },
		wantWritten: true,
	}, {
		name: "a missing finalizer is added",
		vlan: func() *Vlan {
			v := zoneVlan("subnet-a", 101, tenantA, "subnet-a")
			v.Finalizers = nil
			return v
		},
		wantWritten: true,
	}, {
		name: "a Vlan with the finalizer and no timers is left alone",
		vlan: func() *Vlan { return zoneVlan("subnet-a", 101, tenantA, "subnet-a") },
	}} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, tc.vlan())
			v := &Vlan{}
			if err := c.Get(ctx, client.ObjectKey{Name: "subnet-a"}, v); err != nil {
				t.Fatal(err)
			}
			rv := v.ResourceVersion
			if err := adoptVlan(ctx, c, v); err != nil {
				t.Fatal(err)
			}
			got := &Vlan{}
			if err := c.Get(ctx, client.ObjectKey{Name: "subnet-a"}, got); err != nil {
				t.Fatal(err)
			}
			if a, ok := got.Annotations[pxv1.AnnotationDeletedSubnets]; ok {
				t.Fatalf("the adopted Vlan still carries %s=%q", pxv1.AnnotationDeletedSubnets, a)
			}
			if !controllerutil.ContainsFinalizer(got, pxv1.Finalizer) {
				t.Fatalf("the adopted Vlan lacks the finalizer: %v", got.Finalizers)
			}
			if written := got.ResourceVersion != rv; written != tc.wantWritten {
				t.Fatalf("Vlan written = %v, want %v", written, tc.wantWritten)
			}
			before := got.DeepCopy()
			if err := adoptVlan(ctx, c, got); err != nil {
				t.Fatal(err)
			}
			wantUnwritten(t, c, before)
		})
	}

	// A Vlan already being deleted gets no new finalizer, which the API
	// server would refuse, but still loses its timers.
	v := timedVlan(`{"subnet-a":"2026-10-07T07:00:00Z"}`)
	v.Finalizers = []string{"someone-else"}
	c := newClient(t, v)
	if err := c.Delete(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "subnet-a"}, v); err != nil {
		t.Fatal(err)
	}
	if err := adoptVlan(ctx, c, v); err != nil {
		t.Fatal(err)
	}
	got := &Vlan{}
	if err := c.Get(ctx, client.ObjectKey{Name: "subnet-a"}, got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Annotations[pxv1.AnnotationDeletedSubnets]; ok || controllerutil.ContainsFinalizer(got, pxv1.Finalizer) {
		t.Fatalf("a Terminating Vlan must lose its timers and get no finalizer: %+v", got.ObjectMeta)
	}
}

// timedVlan is tenant A's zone Vlan carrying the given prune timers.
func timedVlan(timers string) *Vlan {
	v := zoneVlan("subnet-a", 101, tenantA, "subnet-a")
	v.Annotations = map[string]string{pxv1.AnnotationDeletedSubnets: timers}
	return v
}

// wantUnwritten fails unless the object's stored resourceVersion is still the
// one in o, a copy taken before the pass (a patch writes the new one back into
// the object it is given). The fake client, unlike the API server, bumps it
// even on a patch that changes nothing, so this catches any write.
func wantUnwritten(t *testing.T, c client.Client, o client.Object) {
	t.Helper()
	now, ok := o.DeepCopyObject().(client.Object)
	if !ok {
		t.Fatalf("deep copy of %T is not a client.Object", o)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(o), now); err != nil {
		t.Fatal(err)
	}
	if now.GetResourceVersion() != o.GetResourceVersion() {
		t.Fatalf("%T %s was written (resourceVersion %s -> %s) by a pass that had nothing to change",
			o, o.GetName(), o.GetResourceVersion(), now.GetResourceVersion())
	}
}

func ptr[T any](v T) *T { return &v }
