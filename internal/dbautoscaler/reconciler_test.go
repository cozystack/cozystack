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

package dbautoscaler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/controller-runtime/pkg/client"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

func mustQuantity(s string) resource.Quantity { return resource.MustParse(s) }

func toClientObjects(objs ...runtime.Object) []client.Object {
	out := make([]client.Object, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.(client.Object))
	}
	return out
}

func workloadMonitor(name, ns string, available int32, operational bool) *cozyv1alpha1.WorkloadMonitor {
	return &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector: map[string]string{"app.kubernetes.io/instance": name},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational:       &operational,
			AvailableReplicas: available,
		},
	}
}

// testRESTMapper implements meta.RESTMapper for the reconciler tests.
type testRESTMapper struct{ mapping *meta.RESTMapping }

func (m *testRESTMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return m.mapping, nil
}
func (m *testRESTMapper) RESTMappings(schema.GroupKind, ...string) ([]*meta.RESTMapping, error) {
	return []*meta.RESTMapping{m.mapping}, nil
}
func (m *testRESTMapper) KindFor(schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return m.mapping.GroupVersionKind, nil
}
func (m *testRESTMapper) KindsFor(schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return []schema.GroupVersionKind{m.mapping.GroupVersionKind}, nil
}
func (m *testRESTMapper) ResourceFor(schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return m.mapping.Resource, nil
}
func (m *testRESTMapper) ResourcesFor(schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return []schema.GroupVersionResource{m.mapping.Resource}, nil
}
func (m *testRESTMapper) ResourceSingularizer(r string) (string, error) { return r, nil }

var postgresGVR = schema.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: "postgreses"}

func newPostgresApp(name, ns string, replicas, maxSync int) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "Postgres"})
	u.SetName(name)
	u.SetNamespace(ns)
	u.Object["spec"] = map[string]any{
		"replicas":        int64(replicas),
		"resourcesPreset": "t1.micro",
		"quorum":          map[string]any{"maxSyncReplicas": int64(maxSync), "minSyncReplicas": int64(0)},
	}
	return u
}

func newDHA(name, ns, targetKind string, dryRun bool) *autoscalingv1alpha1.DatabaseHorizontalAutoscaler {
	min, max := int32(2), int32(6)
	return &autoscalingv1alpha1.DatabaseHorizontalAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: autoscalingv1alpha1.DatabaseHorizontalAutoscalerSpec{
			TargetRef:   autoscalingv1alpha1.TargetRef{Kind: targetKind, Name: "db", APIGroup: "apps.cozystack.io"},
			MinReplicas: &min,
			MaxReplicas: &max,
			Metrics: []autoscalingv1alpha1.MetricSpec{{
				Type:   autoscalingv1alpha1.MetricReadConnections,
				Target: autoscalingv1alpha1.MetricTarget{AverageValue: mustQuantity("150")},
			}},
			DryRun: dryRun,
		},
	}
}

type testEnv struct {
	r        *Reconciler
	dyn      *dynamicfake.FakeDynamicClient
	recorder *record.FakeRecorder
	vmValue  string
	vmStatus int
	now      time.Time
}

// setVM mutates the metric value/status the test VM server returns on the next
// query (used to simulate an outage then recovery).
func (e *testEnv) setVM(value string, status int) {
	e.vmValue = value
	e.vmStatus = status
}

// advance moves the injected clock forward (used to cross the convergence deadline).
func (e *testEnv) advance(d time.Duration) { e.now = e.now.Add(d) }

func newTestEnv(t *testing.T, dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, app *unstructured.Unstructured, vmValue string, vmStatus int, objs ...runtime.Object) *testEnv {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = autoscalingv1alpha1.AddToScheme(scheme)

	all := append([]runtime.Object{dha}, objs...)
	c := clientfake.NewClientBuilder().WithScheme(scheme).
		WithObjects(toClientObjects(all...)...).
		WithStatusSubresource(dha).Build()

	dynScheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme,
		map[schema.GroupVersionResource]string{postgresGVR: "PostgresList"}, app)

	env := &testEnv{dyn: dyn, vmValue: vmValue, vmStatus: vmStatus, now: time.Unix(10000, 0)}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if env.vmStatus != http.StatusOK {
			w.WriteHeader(env.vmStatus)
			return
		}
		// The lag and write-activity queries must report quiet so the lag brake
		// does not trip; only the driver metric returns vmValue.
		val := env.vmValue
		q := req.URL.Query().Get("query")
		if strings.Contains(q, "replication_lag") || strings.Contains(q, "in_recovery") {
			val = "0"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,%q]}]}}`, val)
	}))
	t.Cleanup(srv.Close)

	rec := record.NewFakeRecorder(20)
	r := &Reconciler{
		Client:     c,
		Interface:  dyn,
		RESTMapper: &testRESTMapper{mapping: &meta.RESTMapping{Resource: postgresGVR, GroupVersionKind: app.GroupVersionKind(), Scope: meta.RESTScopeNamespace}},
		Scheme:     scheme,
		Recorder:   rec,
		VM:         NewVMClient(),
		Now:        func() time.Time { return env.now },
		BaseURLFor: func(context.Context, string) string { return srv.URL },
	}
	env.r = r
	env.recorder = rec
	return env
}

func (e *testEnv) reconcile(t *testing.T, dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler) *autoscalingv1alpha1.DatabaseHorizontalAutoscaler {
	t.Helper()
	if _, err := e.r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: dha.Namespace, Name: dha.Name}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &autoscalingv1alpha1.DatabaseHorizontalAutoscaler{}
	if err := e.r.Get(context.TODO(), types.NamespacedName{Namespace: dha.Namespace, Name: dha.Name}, got); err != nil {
		t.Fatalf("get dha: %v", err)
	}
	return got
}

func (e *testEnv) appReplicas(t *testing.T, ns, name string) int64 {
	t.Helper()
	obj, err := e.dyn.Resource(postgresGVR).Namespace(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	spec := obj.Object["spec"].(map[string]any)
	switch v := spec["replicas"].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		t.Fatalf("unexpected replicas type %T", v)
		return 0
	}
}

func condStatus(dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, condType string) (string, string) {
	for _, c := range dha.Status.Conditions {
		if c.Type == condType {
			return string(c.Status), c.Reason
		}
	}
	return "", ""
}

func TestReconcileDryRunDoesNotPatch(t *testing.T) {
	dha := newDHA("db", "tenant", "Postgres", true)
	app := newPostgresApp("db", "tenant", 3, 0)
	env := newTestEnv(t, dha, app, "420", http.StatusOK, workloadMonitor("postgres-db", "tenant", 3, true))

	got := env.reconcile(t, dha)
	if got.Status.DesiredReplicas != 4 {
		t.Fatalf("dryRun desired = %d, want 4", got.Status.DesiredReplicas)
	}
	if r := env.appReplicas(t, "tenant", "db"); r != 3 {
		t.Fatalf("dryRun should not patch; app replicas = %d, want 3", r)
	}
}

func TestReconcileScalesUp(t *testing.T) {
	dha := newDHA("db", "tenant", "Postgres", false)
	app := newPostgresApp("db", "tenant", 3, 0)
	env := newTestEnv(t, dha, app, "420", http.StatusOK, workloadMonitor("postgres-db", "tenant", 3, true))

	got := env.reconcile(t, dha)
	if got.Status.DesiredReplicas != 4 {
		t.Fatalf("desired = %d, want 4", got.Status.DesiredReplicas)
	}
	if r := env.appReplicas(t, "tenant", "db"); r != 4 {
		t.Fatalf("app replicas = %d, want 4 (patched)", r)
	}
	if s, _ := condStatus(got, autoscalingv1alpha1.ConditionScalingActive); s != string(metav1.ConditionTrue) {
		t.Fatalf("ScalingActive = %s, want True", s)
	}
}

func TestReconcileShardedNotScalable(t *testing.T) {
	dha := newDHA("db", "tenant", "ClickHouse", false)
	app := newPostgresApp("db", "tenant", 3, 0)
	env := newTestEnv(t, dha, app, "210", http.StatusOK)

	got := env.reconcile(t, dha)
	s, reason := condStatus(got, autoscalingv1alpha1.ConditionScalingActive)
	if s != string(metav1.ConditionFalse) || reason != autoscalingv1alpha1.ReasonSharded {
		t.Fatalf("ScalingActive = %s/%s, want False/Sharded", s, reason)
	}
}

func TestReconcileFailSafeOnVMError(t *testing.T) {
	dha := newDHA("db", "tenant", "Postgres", false)
	app := newPostgresApp("db", "tenant", 3, 0)
	env := newTestEnv(t, dha, app, "", http.StatusInternalServerError, workloadMonitor("postgres-db", "tenant", 3, true))

	got := env.reconcile(t, dha)
	s, reason := condStatus(got, autoscalingv1alpha1.ConditionAbleToScale)
	if s != string(metav1.ConditionFalse) || reason != autoscalingv1alpha1.ReasonMetricUnavailable {
		t.Fatalf("AbleToScale = %s/%s, want False/MetricUnavailable", s, reason)
	}
	if r := env.appReplicas(t, "tenant", "db"); r != 3 {
		t.Fatalf("fail-safe must not patch; replicas = %d", r)
	}
}

func TestReconcileOwnershipConflict(t *testing.T) {
	dha := newDHA("db", "tenant", "Postgres", false)
	app := newPostgresApp("db", "tenant", 5, 0) // observed 5
	env := newTestEnv(t, dha, app, "210", http.StatusOK, workloadMonitor("postgres-db", "tenant", 5, true))

	// Pre-seed state: the autoscaler last wrote 3, so observing 5 means a
	// competing writer changed it.
	st := env.r.stateFor(types.NamespacedName{Namespace: "tenant", Name: "db"})
	three := int32(3)
	st.lastWritten = &three

	got := env.reconcile(t, dha)
	s, reason := condStatus(got, autoscalingv1alpha1.ConditionScalingLimited)
	if s != string(metav1.ConditionTrue) || reason != autoscalingv1alpha1.ReasonOwnershipConflict {
		t.Fatalf("ScalingLimited = %s/%s, want True/OwnershipConflict", s, reason)
	}
	if r := env.appReplicas(t, "tenant", "db"); r != 5 {
		t.Fatalf("must not fight competing writer; replicas = %d, want 5", r)
	}
}

// TestLagBrakeSkippedWhenNoLagQuery guards the Redis fix: an adapter with an
// empty ReplicationLagQuery must never trip the lag brake, even if the metric
// backend would return a large value. Without the fix (non-empty byte-unit
// query) the brake would trip and freeze scaling.
func TestLagBrakeSkippedWhenNoLagQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Everything (incl. a lag query) would look huge.
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"999999"]}]}}`)
	}))
	defer srv.Close()
	r := &Reconciler{VM: NewVMClient(), BaseURLFor: func(context.Context, string) string { return srv.URL }}
	dha := newDHA("db", "tenant", "Redis", false)
	if r.lagBraked(context.TODO(), dha, RedisAdapter{}) {
		t.Fatal("Redis (empty lag query) must not trip the lag brake")
	}
	// sanity: an adapter WITH a lag query and a huge value does brake
	if !r.lagBraked(context.TODO(), dha, PostgresAdapter{}) {
		t.Fatal("Postgres with huge lag + write activity should brake")
	}
}

// TestMetricOutageDoesNotSuppressScaleUp guards the stabilization-history fix: a
// cycle where the metric is unavailable must not record a 0 into the window, or
// the next high-load cycle's scale-up is suppressed for the whole window.
func TestMetricOutageDoesNotSuppressScaleUp(t *testing.T) {
	dha := newDHA("db", "tenant", "Postgres", false)
	app := newPostgresApp("db", "tenant", 3, 0)
	env := newTestEnv(t, dha, app, "420", http.StatusOK, workloadMonitor("postgres-db", "tenant", 3, true))

	// Cycle 1: vmselect down -> fail-safe freeze, must NOT poison history.
	env.setVM("", http.StatusInternalServerError)
	got := env.reconcile(t, dha)
	if s, reason := condStatus(got, autoscalingv1alpha1.ConditionAbleToScale); s != string(metav1.ConditionFalse) || reason != autoscalingv1alpha1.ReasonMetricUnavailable {
		t.Fatalf("cycle1 expected MetricUnavailable freeze, got %s/%s", s, reason)
	}
	// Cycle 2: metric back and high -> must scale up (would be pinned to 3 if the
	// outage recorded a 0 in the up-window).
	env.setVM("420", http.StatusOK)
	got = env.reconcile(t, dha)
	if got.Status.DesiredReplicas != 4 {
		t.Fatalf("after outage recovery desired = %d, want 4 (history was poisoned by the outage)", got.Status.DesiredReplicas)
	}
	if r := env.appReplicas(t, "tenant", "db"); r != 4 {
		t.Fatalf("after outage recovery app replicas = %d, want 4", r)
	}
}

// TestReconcileInvalidTargetFreezes: a non-positive metric target yields a
// distinct InvalidMetricTarget reason (not the misleading MetricUnavailable).
func TestReconcileInvalidTargetFreezes(t *testing.T) {
	min, max := int32(2), int32(6)
	dha := &autoscalingv1alpha1.DatabaseHorizontalAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "tenant"},
		Spec: autoscalingv1alpha1.DatabaseHorizontalAutoscalerSpec{
			TargetRef:   autoscalingv1alpha1.TargetRef{Kind: "Postgres", Name: "db", APIGroup: "apps.cozystack.io"},
			MinReplicas: &min,
			MaxReplicas: &max,
			Metrics: []autoscalingv1alpha1.MetricSpec{{
				Type:   autoscalingv1alpha1.MetricReadConnections,
				Target: autoscalingv1alpha1.MetricTarget{AverageValue: mustQuantity("0")},
			}},
		},
	}
	app := newPostgresApp("db", "tenant", 3, 0)
	env := newTestEnv(t, dha, app, "420", http.StatusOK, workloadMonitor("postgres-db", "tenant", 3, true))
	got := env.reconcile(t, dha)
	s, reason := condStatus(got, autoscalingv1alpha1.ConditionAbleToScale)
	if s != string(metav1.ConditionFalse) || reason != autoscalingv1alpha1.ReasonInvalidTarget {
		t.Fatalf("AbleToScale = %s/%s, want False/InvalidMetricTarget", s, reason)
	}
	if r := env.appReplicas(t, "tenant", "db"); r != 3 {
		t.Fatalf("invalid target must not patch; replicas = %d", r)
	}
}

// TestOwnershipBackoffSurvivesRestart guards the durability fix: a fresh
// controller (empty in-memory state) must reconstruct the ownership back-off
// from status.lastAppliedReplicas, so a competing writer's value is not
// silently clobbered after a restart/leader failover.
func TestOwnershipBackoffSurvivesRestart(t *testing.T) {
	dha := newDHA("db", "tenant", "Postgres", false)
	three := int32(3)
	dha.Status.LastAppliedReplicas = &three     // operator last wrote 3, before the restart
	app := newPostgresApp("db", "tenant", 5, 0) // a competing writer set it to 5
	env := newTestEnv(t, dha, app, "420", http.StatusOK, workloadMonitor("postgres-db", "tenant", 5, true))
	// Persist the status (WithStatusSubresource ignores the initial status).
	cur := &autoscalingv1alpha1.DatabaseHorizontalAutoscaler{}
	if err := env.r.Get(context.TODO(), types.NamespacedName{Namespace: "tenant", Name: "db"}, cur); err != nil {
		t.Fatalf("get: %v", err)
	}
	cur.Status.LastAppliedReplicas = &three
	if err := env.r.Status().Update(context.TODO(), cur); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	// Fresh state map (simulating a new leader): no in-memory lastWritten.
	env.r.state = nil
	got := env.reconcile(t, dha)
	s, reason := condStatus(got, autoscalingv1alpha1.ConditionScalingLimited)
	if s != string(metav1.ConditionTrue) || reason != autoscalingv1alpha1.ReasonOwnershipConflict {
		t.Fatalf("after restart ScalingLimited = %s/%s, want True/OwnershipConflict", s, reason)
	}
	if r := env.appReplicas(t, "tenant", "db"); r != 5 {
		t.Fatalf("must not clobber competing writer after restart; replicas = %d, want 5", r)
	}
}

// TestReconcileStuckScalingRollback drives the full stuck-scaling flow end to
// end: scale-up patch -> never converges (availableReplicas stays behind) ->
// past the convergence deadline the operator rolls replicas back to the last
// converged count and surfaces StuckScaling.
func TestReconcileStuckScalingRollback(t *testing.T) {
	dha := newDHA("db", "tenant", "Postgres", false)
	dl := int32(900)
	dha.Spec.Behavior = &autoscalingv1alpha1.Behavior{ConvergenceDeadlineSeconds: &dl}
	app := newPostgresApp("db", "tenant", 3, 0)
	// WorkloadMonitor is stuck reporting available=3 (the new standby never comes up).
	env := newTestEnv(t, dha, app, "420", http.StatusOK, workloadMonitor("postgres-db", "tenant", 3, true))

	// Cycle 1: converged at 3, high load -> scale up to 4.
	got := env.reconcile(t, dha)
	if got.Status.DesiredReplicas != 4 || env.appReplicas(t, "tenant", "db") != 4 {
		t.Fatalf("cycle1 expected scale-up to 4, desired=%d app=%d", got.Status.DesiredReplicas, env.appReplicas(t, "tenant", "db"))
	}

	// Cycle 2 (immediately): replicas=4 but available=3 -> single-flight freeze.
	got = env.reconcile(t, dha)
	if s, reason := condStatus(got, autoscalingv1alpha1.ConditionAbleToScale); s != string(metav1.ConditionFalse) || reason != autoscalingv1alpha1.ReasonScaleInFlight {
		t.Fatalf("cycle2 expected ScaleInFlight freeze, got %s/%s", s, reason)
	}
	if env.appReplicas(t, "tenant", "db") != 4 {
		t.Fatalf("cycle2 must not change replicas")
	}

	// Cycle 3: past the convergence deadline -> roll back to lastConverged (3).
	env.advance(1000 * time.Second)
	got = env.reconcile(t, dha)
	if s, reason := condStatus(got, autoscalingv1alpha1.ConditionAbleToScale); s != string(metav1.ConditionFalse) || reason != autoscalingv1alpha1.ReasonStuckScaling {
		t.Fatalf("cycle3 expected StuckScaling, got %s/%s", s, reason)
	}
	if r := env.appReplicas(t, "tenant", "db"); r != 3 {
		t.Fatalf("cycle3 expected rollback to 3, app replicas = %d", r)
	}

	// Cycle 4: load still high so the decision again wants 4 (the failed size),
	// but the backoff must suppress the re-attempt — replicas stay at 3, no thrash.
	env.advance(30 * time.Second) // within backoff window
	got = env.reconcile(t, dha)
	if r := env.appReplicas(t, "tenant", "db"); r != 3 {
		t.Fatalf("cycle4 backoff should suppress re-attempt; app replicas = %d, want 3", r)
	}
	if s, reason := condStatus(got, autoscalingv1alpha1.ConditionAbleToScale); s != string(metav1.ConditionFalse) || reason != autoscalingv1alpha1.ReasonStuckScaling {
		t.Fatalf("cycle4 expected StuckScaling (backoff), got %s/%s", s, reason)
	}
}
