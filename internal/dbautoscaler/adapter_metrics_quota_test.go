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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
)

func TestPostgresAdapterBasics(t *testing.T) {
	a := PostgresAdapter{}
	if a.Kind() != "Postgres" || a.ReplicasPath() != "replicas" || a.PrimaryCount() != 1 {
		t.Fatalf("unexpected adapter basics: %s/%s/%d", a.Kind(), a.ReplicasPath(), a.PrimaryCount())
	}
	if ok, _ := a.Scalable(map[string]any{}); !ok {
		t.Fatalf("postgres should be scalable")
	}
}

func TestPostgresQuorumFloor(t *testing.T) {
	a := PostgresAdapter{}
	// maxSyncReplicas 2 => floor 3.
	values := map[string]any{"quorum": map[string]any{"maxSyncReplicas": float64(2)}}
	if got := a.QuorumFloor(values); got != 3 {
		t.Fatalf("QuorumFloor = %d, want 3", got)
	}
	// absent => 0+1 = 1.
	if got := a.QuorumFloor(map[string]any{}); got != 1 {
		t.Fatalf("QuorumFloor default = %d, want 1", got)
	}
}

func TestPostgresDriverQueryIsNamespaceScoped(t *testing.T) {
	a := PostgresAdapter{}
	app := types.NamespacedName{Namespace: "tenant-acme", Name: "db"}
	for _, m := range []autoscalingv1alpha1.MetricType{
		autoscalingv1alpha1.MetricReadConnections,
		autoscalingv1alpha1.MetricReadCPUUtilization,
	} {
		q := a.DriverQuery(app, m)
		if !strings.Contains(q, `namespace="tenant-acme"`) {
			t.Errorf("driver query for %s is not namespace-scoped: %s", m, q)
		}
		if !strings.Contains(q, "db") {
			t.Errorf("driver query for %s does not reference the target: %s", m, q)
		}
	}
	// Lag query must also be namespace-scoped (tenant isolation).
	if !strings.Contains(a.ReplicationLagQuery(app), `namespace="tenant-acme"`) {
		t.Errorf("lag query not namespace-scoped: %s", a.ReplicationLagQuery(app))
	}
}

func TestAdapterForUnknownKind(t *testing.T) {
	if AdapterFor("ClickHouse") != nil {
		t.Fatalf("ClickHouse should have no adapter (sharded)")
	}
	if AdapterFor("Postgres") == nil {
		t.Fatalf("Postgres should have an adapter")
	}
}

func TestVMQueryScalarSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1713000000,"210"]}]}}`)
	}))
	defer srv.Close()
	c := NewVMClient()
	v, ok, err := c.QueryScalar(context.TODO(), srv.URL, `sum(x)`)
	if err != nil || !ok || v != 210 {
		t.Fatalf("QueryScalar = %v,%v,%v; want 210,true,nil", v, ok, err)
	}
}

func TestVMQueryScalarEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv.Close()
	_, ok, err := NewVMClient().QueryScalar(context.TODO(), srv.URL, `sum(x)`)
	if ok || err != nil {
		t.Fatalf("empty result should be ok=false,err=nil; got %v,%v", ok, err)
	}
}

func TestVMQueryScalarServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, ok, err := NewVMClient().QueryScalar(context.TODO(), srv.URL, `sum(x)`)
	if ok || err != nil {
		t.Fatalf("500 should degrade to ok=false,err=nil; got %v,%v", ok, err)
	}
}

func TestVMQueryScalarNoURL(t *testing.T) {
	_, ok, err := NewVMClient().QueryScalar(context.TODO(), "", `sum(x)`)
	if ok || err != nil {
		t.Fatalf("no URL should be ok=false,err=nil; got %v,%v", ok, err)
	}
}

func TestResolveVMSelectURL(t *testing.T) {
	got := ResolveVMSelectURL("tenant-root")
	want := "http://vmselect-shortterm.tenant-root.svc:8481/select/0/prometheus"
	if got != want {
		t.Fatalf("ResolveVMSelectURL = %s, want %s", got, want)
	}
}

func TestPerPodResourcesPreset(t *testing.T) {
	cpu, mem, ok := PerPodResources(map[string]any{"resourcesPreset": "t1.micro"})
	if !ok || cpu.MilliValue() != 500 || mem.Value() != 256*1024*1024 {
		t.Fatalf("t1.micro = %v/%v ok=%v", cpu.String(), mem.String(), ok)
	}
	// Explicit resources win over the preset.
	cpu, _, ok = PerPodResources(map[string]any{
		"resourcesPreset": "t1.micro",
		"resources":       map[string]any{"cpu": "2", "memory": "4Gi"},
	})
	if !ok || cpu.MilliValue() != 2000 {
		t.Fatalf("explicit resources cpu = %v ok=%v", cpu.String(), ok)
	}
	// Default when nothing set.
	if _, _, ok := PerPodResources(map[string]any{}); !ok {
		t.Fatalf("default preset should resolve")
	}
}

func TestMaxReplicasWithinQuota(t *testing.T) {
	// hard 4 cpu, used 1 cpu, per-pod 1 cpu => 3 additional => current(2)+3 = 5.
	rq := corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "q", Namespace: "tenant"},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{corev1.ResourceRequestsCPU: resource.MustParse("4")},
			Used: corev1.ResourceList{corev1.ResourceRequestsCPU: resource.MustParse("1")},
		},
	}
	got := MaxReplicasWithinQuota([]corev1.ResourceQuota{rq}, 2, resource.MustParse("1"), resource.MustParse("1Gi"))
	if got == nil || *got != 5 {
		t.Fatalf("MaxReplicasWithinQuota = %v, want 5", got)
	}

	// No quota => unbounded (nil).
	if got := MaxReplicasWithinQuota(nil, 2, resource.MustParse("1"), resource.MustParse("1Gi")); got != nil {
		t.Fatalf("no quota should be unbounded, got %v", *got)
	}
}
