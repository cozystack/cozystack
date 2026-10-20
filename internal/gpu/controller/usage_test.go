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

package controller

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	cozycontroller "github.com/cozystack/cozystack/internal/controller"
	"github.com/cozystack/cozystack/internal/gpu"
)

func (e *env) record(claim string) *cozyv1alpha1.Workload {
	e.t.Helper()
	w := &cozyv1alpha1.Workload{}
	if err := e.c.Get(context.Background(), key(ns, "gpu-"+claim), w); err != nil {
		return nil
	}
	return w
}

func (e *env) recordExists(claim string) bool { return e.record(claim) != nil }

func TestUsageRecordExistsExactlyWhileBoundOrAttached(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		claim("bound", "l40s", 2, bound("n1")),
		claim("attached", "l40s", 1, bound("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
		claim("pending", "l40s", 4),
		claim("lost", "l40s", 1, lost("gone")),
	)
	e.pass()
	for name, want := range map[string]bool{"bound": true, "attached": true, "pending": false, "lost": false} {
		if got := e.recordExists(name); got != want {
			t.Errorf("record for %s exists = %v, want %v", name, got, want)
		}
	}
	rec := e.record("bound")
	if rec.Status.Kind != gpu.WorkloadKind || len(rec.Labels) != 0 {
		t.Fatalf("record %+v", rec)
	}
	if q := rec.Status.Resources["gpu.cozystack.io/l40s"]; q.Cmp(resource.MustParse("2")) != 0 {
		t.Fatalf("resources %v, want gpu.cozystack.io/l40s: 2", rec.Status.Resources)
	}
	monitors := &cozyv1alpha1.WorkloadMonitorList{}
	if err := e.c.List(t.Context(), monitors); err != nil || len(monitors.Items) != 0 {
		t.Fatalf("GPU usage records need no WorkloadMonitor, found %d (%v)", len(monitors.Items), err)
	}
}

// The record's owner is its claim, so garbage collection removes it with
// the claim even when this controller is down.
func TestUsageRecordIsOwnedByItsClaim(t *testing.T) {
	d := claim("a", "l40s", 1, bound("n1"))
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), d)
	e.pass()
	refs := e.record("a").OwnerReferences
	if len(refs) != 1 || refs[0].Kind != "GPUClaim" || refs[0].APIVersion != gpuv1alpha1.GroupVersion.String() ||
		refs[0].UID != d.UID || refs[0].Name != "a" {
		t.Fatalf("owner references %+v do not name the claim", refs)
	}
	e.delete(d)
	e.pass()
	if e.recordExists("a") {
		t.Fatal("record kept after its claim was released")
	}
}

func TestUsageRecordIgnoresTenantLabelsOnTheClaim(t *testing.T) {
	d := claim("a", "l40s", 1, bound("n1"))
	d.Labels = map[string]string{"workloads.cozystack.io/monitor": "other", "team": "x"}
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), d)
	e.pass()
	if rec := e.record("a"); rec == nil || len(rec.Labels) != 0 {
		t.Fatalf("record labels %v", rec)
	}
}

func TestUsageRecordGoesWhenTheClaimIsLost(t *testing.T) {
	n1 := node("n1", map[string]int64{rnL40S: 1})
	e := newEnv(t,
		class("l40s", rnL40S),
		n1,
		claim("a", "l40s", 1, bound("n1")),
	)
	e.pass()
	if !e.recordExists("a") {
		t.Fatal("a Bound claim has no usage record")
	}
	e.delete(n1)
	e.pass()
	expectPlacement(t, e.claim("a"), gpuv1alpha1.GPUClaimLost, "n1")
	if e.recordExists("a") {
		t.Fatal("a Lost claim keeps its usage record")
	}
}

// A GPU record has no monitor label, which the Workload controller would
// otherwise read as an orphan; it leaves a record owned by a GPUClaim to
// its owner.
func TestUsageRecordSurvivesWorkloadReconciles(t *testing.T) {
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), claim("a", "l40s", 1, bound("n1")))
	e.pass()
	workloads := &cozycontroller.WorkloadReconciler{Client: e.c, Scheme: e.c.Scheme()}
	if _, err := workloads.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(ns, "gpu-a")}); err != nil {
		t.Fatalf("Workload reconcile: %v", err)
	}
	rec := e.record("a")
	if rec == nil || rec.Status.Kind != gpu.WorkloadKind {
		t.Fatalf("record after the reconcile: %+v", rec)
	}
}

// Template claim names reach 253 characters for long VMI names, and such a
// claim still needs a usage record.
func TestUsageRecordOfAClaimWithAMaximalNameFitsAndIsStable(t *testing.T) {
	// The cut lands right after the dot, which must not end a name part.
	long := strings.Repeat("w", 237) + "." + strings.Repeat("y", 15)
	if len(long) != 253 {
		t.Fatalf("fixture is %d characters", len(long))
	}
	d := claim(long, "l40s", 1, bound("n1"))
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), d)
	e.pass()
	name := recordName(d)
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		t.Fatalf("record name %q: %v", name, errs)
	}
	if other := claim(long[:252]+"z", "l40s", 1); recordName(other) == name {
		t.Fatal("two claims differing only past the cut share a record")
	}
	rec := &cozyv1alpha1.Workload{}
	if err := e.c.Get(t.Context(), key(ns, name), rec); err != nil {
		t.Fatalf("record: %v", err)
	}
	e.pass()
	records := &cozyv1alpha1.WorkloadList{}
	_ = e.c.List(t.Context(), records)
	if len(records.Items) != 1 {
		t.Fatalf("%d records after a second pass, want 1", len(records.Items))
	}
}

// A tenant names its own claims, so it can name one after the hashed
// record of another; neither that nor two long names sharing the cut may
// make two held claims share one record.
func TestUsageRecordNamesOfLongAndCraftedClaimsDoNotCollide(t *testing.T) {
	long := strings.Repeat("a", 253)
	crafted := strings.TrimPrefix(recordName(claim(long, "l40s", 1)), "gpu-")
	crafted = strings.TrimPrefix(crafted, "gpuh-")
	if len(crafted) > 253 {
		t.Fatalf("fixture too long: %d", len(crafted))
	}
	twin := strings.Repeat("a", 238) + strings.Repeat("b", 15)
	names := map[string]string{}
	for _, n := range []string{long, crafted, twin} {
		r := recordName(claim(n, "l40s", 1))
		if other, ok := names[r]; ok {
			t.Fatalf("claims %q and %q share record %q", other, n, r)
		}
		names[r] = n
		if errs := validation.IsDNS1123Subdomain(r); len(errs) > 0 {
			t.Fatalf("record %q: %v", r, errs)
		}
	}
	if !strings.HasPrefix(recordName(claim(long, "l40s", 1)), "gpuh-") {
		t.Fatal("a hashed record name must carry its own prefix")
	}
	if h := strings.TrimPrefix(recordName(claim(long, "l40s", 1)), "gpuh-"); len(h) < 33 || len(strings.Split(h, "-")[len(strings.Split(h, "-"))-1]) != 32 {
		t.Fatalf("hashed record %q must end in a 128-bit hash", h)
	}
}

// A record a stale cache shows as missing already exists; the create's
// AlreadyExists is not a failure of the pass.
func TestAUsageRecordCreateOnAStaleCacheIsNotAnError(t *testing.T) {
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), claim("a", "l40s", 1, bound("n1")))
	e.r.Client = interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*cozyv1alpha1.Workload); ok {
				return apierrors.NewAlreadyExists(cozyv1alpha1.GroupVersion.WithResource("workloads").GroupResource(), obj.GetName())
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	if _, err := e.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: passKey}); err != nil {
		t.Fatalf("pass: %v", err)
	}
}
