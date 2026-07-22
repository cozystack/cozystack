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
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// startEnvtest boots an apiserver-backed environment with the DHA CRD, the
// WorkloadMonitor CRD, and a fixture Postgres CRD (standing in for the
// aggregated apps API, which the real cozystack-api extension server serves and
// which envtest cannot host). It skips the test when envtest binaries are not
// installed, so `go test ./internal/...` still passes locally without assets;
// CI provisions them via setup-envtest.
func startEnvtest(t *testing.T) (*rest.Config, func()) {
	t.Helper()
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "packages", "system", "db-autoscaler", "definitions"),
			filepath.Join("..", "..", "packages", "system", "cozystack-controller", "definitions"),
			filepath.Join("testdata", "crds"),
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.31.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}
	cfg, err := env.Start()
	if err != nil {
		t.Skipf("envtest unavailable (install with setup-envtest): %v", err)
	}
	return cfg, func() { _ = env.Stop() }
}

func newEnvtestReconciler(t *testing.T, cfg *rest.Config, baseURL string) (*Reconciler, client.Client, dynamic.Interface) {
	t.Helper()
	_ = corev1.AddToScheme(scheme.Scheme)
	_ = cozyv1alpha1.AddToScheme(scheme.Scheme)
	_ = autoscalingv1alpha1.AddToScheme(scheme.Scheme)

	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic: %v", err)
	}
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatalf("http client: %v", err)
	}
	mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		t.Fatalf("mapper: %v", err)
	}
	r := &Reconciler{
		Client:     c,
		Interface:  dyn,
		RESTMapper: mapper,
		Scheme:     scheme.Scheme,
		Recorder:   record.NewFakeRecorder(20),
		VM:         NewVMClient(),
		Now:        func() time.Time { return time.Unix(10000, 0) },
		BaseURLFor: func(context.Context, string) string { return baseURL },
	}
	return r, c, dyn
}

func TestEnvtestDHASchemaRejectsMinReplicasBelowTwo(t *testing.T) {
	cfg, stop := startEnvtest(t)
	defer stop()
	_, c, _ := newEnvtestReconciler(t, cfg, "")

	ctx := context.Background()
	mkNamespace(t, ctx, c, "tenant-a")

	one := int32(1)
	dha := &autoscalingv1alpha1.DatabaseHorizontalAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "tenant-a"},
		Spec: autoscalingv1alpha1.DatabaseHorizontalAutoscalerSpec{
			TargetRef:   autoscalingv1alpha1.TargetRef{Kind: "Postgres", Name: "db"},
			MinReplicas: &one, // violates Minimum=2
			MaxReplicas: &one,
			Metrics: []autoscalingv1alpha1.MetricSpec{{
				Type:   autoscalingv1alpha1.MetricReadConnections,
				Target: autoscalingv1alpha1.MetricTarget{AverageValue: mustQuantity("150")},
			}},
		},
	}
	err := c.Create(ctx, dha)
	if err == nil {
		t.Fatalf("expected the apiserver to reject minReplicas=1")
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("expected Invalid error, got %v", err)
	}
}

func TestEnvtestDHASchemaRejectsMaxBelowMin(t *testing.T) {
	cfg, stop := startEnvtest(t)
	defer stop()
	_, c, _ := newEnvtestReconciler(t, cfg, "")
	ctx := context.Background()
	mkNamespace(t, ctx, c, "tenant-c")

	min, max := int32(6), int32(2) // max < min violates the CEL rule
	dha := &autoscalingv1alpha1.DatabaseHorizontalAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "tenant-c"},
		Spec: autoscalingv1alpha1.DatabaseHorizontalAutoscalerSpec{
			TargetRef:   autoscalingv1alpha1.TargetRef{Kind: "Postgres", Name: "db"},
			MinReplicas: &min,
			MaxReplicas: &max,
			Metrics: []autoscalingv1alpha1.MetricSpec{{
				Type:   autoscalingv1alpha1.MetricReadConnections,
				Target: autoscalingv1alpha1.MetricTarget{AverageValue: mustQuantity("150")},
			}},
		},
	}
	err := c.Create(ctx, dha)
	if err == nil || !apierrors.IsInvalid(err) {
		t.Fatalf("expected the apiserver to reject maxReplicas<minReplicas with Invalid, got %v", err)
	}
}

func TestEnvtestReconcilePatchesReplicas(t *testing.T) {
	cfg, stop := startEnvtest(t)
	defer stop()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		val := "420" // avg 210 over 2 read replicas => desired 4
		if q := req.URL.Query().Get("query"); strings.Contains(q, "replication_lag") || strings.Contains(q, "in_recovery") {
			val = "0"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,%q]}]}}`, val)
	}))
	defer srv.Close()

	r, c, dyn := newEnvtestReconciler(t, cfg, srv.URL)
	ctx := context.Background()
	mkNamespace(t, ctx, c, "tenant-b")

	// Fixture Postgres app with replicas 3.
	gvr := schema.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: "postgreses"}
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "Postgres"})
	app.SetName("db")
	app.SetNamespace("tenant-b")
	app.Object["spec"] = map[string]any{"replicas": int64(3), "resourcesPreset": "t1.micro"}
	if _, err := dyn.Resource(gvr).Namespace("tenant-b").Create(ctx, app, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// WorkloadMonitor: operational, converged at 3.
	if err := c.Create(ctx, workloadMonitor("postgres-db", "tenant-b", 3, true)); err != nil {
		t.Fatalf("create wm: %v", err)
	}
	// Status subresource must be set explicitly.
	wm := &cozyv1alpha1.WorkloadMonitor{}
	_ = c.Get(ctx, types.NamespacedName{Namespace: "tenant-b", Name: "postgres-db"}, wm)
	op := true
	wm.Status.Operational = &op
	wm.Status.AvailableReplicas = 3
	_ = c.Status().Update(ctx, wm)

	min, max := int32(2), int32(6)
	dha := &autoscalingv1alpha1.DatabaseHorizontalAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "tenant-b"},
		Spec: autoscalingv1alpha1.DatabaseHorizontalAutoscalerSpec{
			TargetRef:   autoscalingv1alpha1.TargetRef{Kind: "Postgres", Name: "db"},
			MinReplicas: &min,
			MaxReplicas: &max,
			Metrics: []autoscalingv1alpha1.MetricSpec{{
				Type:   autoscalingv1alpha1.MetricReadConnections,
				Target: autoscalingv1alpha1.MetricTarget{AverageValue: mustQuantity("150")},
			}},
		},
	}
	if err := c.Create(ctx, dha); err != nil {
		t.Fatalf("create dha: %v", err)
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-b", Name: "db"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, err := dyn.Resource(gvr).Namespace("tenant-b").Get(ctx, "db", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	if replicas != 4 {
		t.Fatalf("app replicas = %d, want 4 (patched)", replicas)
	}
	// Marker annotation must be stamped.
	if got.GetAnnotations()[autoscalingv1alpha1.ManagedByAnnotation] != "db" {
		t.Fatalf("missing managed-by marker: %v", got.GetAnnotations())
	}
}

func mkNamespace(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
}
