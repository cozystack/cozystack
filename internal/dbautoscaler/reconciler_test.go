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
}

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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if vmStatus != http.StatusOK {
			w.WriteHeader(vmStatus)
			return
		}
		// The lag and write-activity queries must report quiet so the lag brake
		// does not trip; only the driver metric returns vmValue.
		val := vmValue
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
		Now:        func() time.Time { return time.Unix(10000, 0) },
		BaseURLFor: func(context.Context, string) string { return srv.URL },
	}
	return &testEnv{r: r, dyn: dyn, recorder: rec}
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
