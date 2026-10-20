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

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	cozycontroller "github.com/cozystack/cozystack/internal/controller"
	"github.com/cozystack/cozystack/internal/gpu"
)

func (e *env) record(device string) *cozyv1alpha1.Workload {
	e.t.Helper()
	w := &cozyv1alpha1.Workload{}
	if err := e.c.Get(context.Background(), key(ns, "gpu-"+device), w); err != nil {
		return nil
	}
	return w
}

func (e *env) recordExists(device string) bool { return e.record(device) != nil }

func TestUsageRecordExistsExactlyWhileBoundOrAttached(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 2}),
		device("bound", "l40s", 2, bound("n1")),
		device("attached", "l40s", 1, bound("n1"), attached("vm1")),
		vmiObj("vm1", "Running", "n1", ""),
		device("pending", "l40s", 4),
		device("lost", "l40s", 1, lost("gone")),
	)
	e.pass()
	for name, want := range map[string]bool{"bound": true, "attached": true, "pending": false, "lost": false} {
		if got := e.recordExists(name); got != want {
			t.Errorf("record for %s exists = %v, want %v", name, got, want)
		}
	}
	rec := e.record("bound")
	if rec.Status.Kind != gpu.WorkloadKind || rec.Labels[workloadMonitorLabel] != gpu.UsageMonitorName {
		t.Fatalf("record %+v", rec)
	}
	if q := rec.Status.Resources["gpu.cozystack.io/l40s"]; q.Cmp(resource.MustParse("2")) != 0 {
		t.Fatalf("resources %v, want gpu.cozystack.io/l40s: 2", rec.Status.Resources)
	}
	m := &cozyv1alpha1.WorkloadMonitor{}
	if err := e.c.Get(t.Context(), key(ns, gpu.UsageMonitorName), m); err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if len(m.Spec.Selector) != 1 || m.Spec.Selector[gpu.LabelUsageMonitor] == "" {
		t.Fatalf("monitor selector %v must be the reserved label alone", m.Spec.Selector)
	}
}

func TestUsageRecordIgnoresTenantLabelsOnTheDevice(t *testing.T) {
	d := device("a", "l40s", 1, bound("n1"))
	d.Labels = map[string]string{"workloads.cozystack.io/monitor": "other", "team": "x"}
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), d)
	e.pass()
	rec := e.record("a")
	if rec == nil || len(rec.Labels) != 1 || rec.Labels[workloadMonitorLabel] != gpu.UsageMonitorName {
		t.Fatalf("record labels %v", rec)
	}
}

func TestUsageRecordGoesWhenTheDeviceIsLost(t *testing.T) {
	e := newEnv(t,
		class("l40s", rnL40S),
		node("n1", map[string]int64{rnL40S: 1}),
		device("a", "l40s", 1, bound("gone")),
		device("b", "l40s", 1, bound("n1"), inNamespace("tenant-other")),
	)
	e.pass()
	expectPlacement(t, e.device("a"), gpuv1alpha1.GPUDeviceLost, "gone")
	if e.recordExists("a") {
		t.Fatal("a Lost device keeps its usage record")
	}
}

// The GPU records sit under a monitor that selects nothing, so neither the
// WorkloadMonitor nor the Workload controller has anything to remove.
func TestUsageRecordSurvivesMonitorAndWorkloadReconciles(t *testing.T) {
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), device("a", "l40s", 1, bound("n1")))
	e.pass()
	ctx := context.Background()
	monitor := &cozycontroller.WorkloadMonitorReconciler{Client: e.c, Scheme: e.c.Scheme()}
	if _, err := monitor.Reconcile(ctx, ctrl.Request{NamespacedName: key(ns, gpu.UsageMonitorName)}); err != nil {
		t.Fatalf("WorkloadMonitor reconcile: %v", err)
	}
	workloads := &cozycontroller.WorkloadReconciler{Client: e.c, Scheme: e.c.Scheme()}
	if _, err := workloads.Reconcile(ctx, ctrl.Request{NamespacedName: key(ns, "gpu-a")}); err != nil {
		t.Fatalf("Workload reconcile: %v", err)
	}
	rec := e.record("a")
	if rec == nil || rec.Status.Kind != gpu.WorkloadKind {
		t.Fatalf("record after the reconciles: %+v", rec)
	}
}

func TestMonitorIsRemovedWithTheNamespacesLastDevice(t *testing.T) {
	d := device("a", "l40s", 1, bound("n1"))
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), d)
	e.pass()
	e.delete(d)
	e.pass()
	if !e.gone(&cozyv1alpha1.WorkloadMonitor{ObjectMeta: metaName(ns, gpu.UsageMonitorName)}) {
		t.Fatal("monitor kept in a namespace without devices")
	}
}

// Template device names reach 253 characters for long VMI names, and such a
// device still needs a usage record.
func TestUsageRecordOfADeviceWithAMaximalNameFitsAndIsStable(t *testing.T) {
	// The cut lands right after the dot, which must not end a name part.
	long := strings.Repeat("w", 237) + "." + strings.Repeat("y", 15)
	if len(long) != 253 {
		t.Fatalf("fixture is %d characters", len(long))
	}
	d := device(long, "l40s", 1, bound("n1"))
	e := newEnv(t, class("l40s", rnL40S), node("n1", map[string]int64{rnL40S: 2}), d)
	e.pass()
	name := recordName(d)
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		t.Fatalf("record name %q: %v", name, errs)
	}
	if other := device(long[:252]+"z", "l40s", 1); recordName(other) == name {
		t.Fatal("two devices differing only past the cut share a record")
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

// A tenant names its own devices, so it can name one after the hashed
// record of another; neither that nor two long names sharing the cut may
// make two held devices share one record.
func TestUsageRecordNamesOfLongAndCraftedDevicesDoNotCollide(t *testing.T) {
	long := strings.Repeat("a", 253)
	crafted := strings.TrimPrefix(recordName(device(long, "l40s", 1)), "gpu-")
	crafted = strings.TrimPrefix(crafted, "gpuh-")
	if len(crafted) > 253 {
		t.Fatalf("fixture too long: %d", len(crafted))
	}
	twin := strings.Repeat("a", 238) + strings.Repeat("b", 15)
	names := map[string]string{}
	for _, n := range []string{long, crafted, twin} {
		r := recordName(device(n, "l40s", 1))
		if other, ok := names[r]; ok {
			t.Fatalf("devices %q and %q share record %q", other, n, r)
		}
		names[r] = n
		if errs := validation.IsDNS1123Subdomain(r); len(errs) > 0 {
			t.Fatalf("record %q: %v", r, errs)
		}
	}
	if !strings.HasPrefix(recordName(device(long, "l40s", 1)), "gpuh-") {
		t.Fatal("a hashed record name must carry its own prefix")
	}
	if h := strings.TrimPrefix(recordName(device(long, "l40s", 1)), "gpuh-"); len(h) < 33 || len(strings.Split(h, "-")[len(strings.Split(h, "-"))-1]) != 32 {
		t.Fatalf("hashed record %q must end in a 128-bit hash", h)
	}
}
