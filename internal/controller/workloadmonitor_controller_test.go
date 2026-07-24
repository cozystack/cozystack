package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	cosiv1alpha1 "sigs.k8s.io/container-object-storage-interface-api/apis/objectstorage/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcile_OperationalStatusPersisted(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = cosiv1alpha1.AddToScheme(scheme)

	minReplicas := int32(2)
	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-monitor",
			Namespace: "default",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector:    map[string]string{"app": "test"},
			MinReplicas: &minReplicas,
		},
	}

	// Create one pod that is ready — availableReplicas=1 < minReplicas=2, so Operational should be false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, pod).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-monitor", Namespace: "default"}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Fetch the monitor back from fake client and check Operational is persisted
	updated := &cozyv1alpha1.WorkloadMonitor{}
	if err := fakeClient.Get(context.TODO(), req.NamespacedName, updated); err != nil {
		t.Fatalf("Failed to get updated WorkloadMonitor: %v", err)
	}

	if updated.Status.Operational == nil {
		t.Fatal("Expected Operational to be set, got nil")
	}
	if *updated.Status.Operational {
		t.Error("Expected Operational=false (1 available < 2 minReplicas), got true")
	}
	if updated.Status.ObservedReplicas != 1 {
		t.Errorf("Expected ObservedReplicas=1, got %d", updated.Status.ObservedReplicas)
	}
	if updated.Status.AvailableReplicas != 1 {
		t.Errorf("Expected AvailableReplicas=1, got %d", updated.Status.AvailableReplicas)
	}
}

func TestReconcile_OperationalTrue_WhenEnoughReplicas(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = cosiv1alpha1.AddToScheme(scheme)

	minReplicas := int32(1)
	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-monitor",
			Namespace: "default",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector:    map[string]string{"app": "test"},
			MinReplicas: &minReplicas,
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, pod).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-monitor", Namespace: "default"}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	updated := &cozyv1alpha1.WorkloadMonitor{}
	if err := fakeClient.Get(context.TODO(), req.NamespacedName, updated); err != nil {
		t.Fatalf("Failed to get updated WorkloadMonitor: %v", err)
	}

	if updated.Status.Operational == nil {
		t.Fatal("Expected Operational to be set, got nil")
	}
	if !*updated.Status.Operational {
		t.Error("Expected Operational=true (1 available >= 1 minReplicas), got false")
	}
}

func TestGetMonitorLabels(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected map[string]string
	}{
		{
			name:     "nil labels",
			labels:   nil,
			expected: map[string]string{},
		},
		{
			name: "only workloads.cozystack.io/* labels are propagated",
			labels: map[string]string{
				"workloads.cozystack.io/resource-preset": "medium",
				"app.kubernetes.io/name":                 "postgres",
				"custom.example.com/team":                "platform",
			},
			expected: map[string]string{
				"workloads.cozystack.io/resource-preset": "medium",
			},
		},
		{
			name: "monitor label is reserved and excluded",
			labels: map[string]string{
				"workloads.cozystack.io/resource-preset": "small",
				"workloads.cozystack.io/monitor":         "should-be-dropped",
			},
			expected: map[string]string{
				"workloads.cozystack.io/resource-preset": "small",
			},
		},
		{
			name: "multiple workloads.cozystack.io labels propagate",
			labels: map[string]string{
				"workloads.cozystack.io/resource-preset": "large",
				"workloads.cozystack.io/tier":            "db",
			},
			expected: map[string]string{
				"workloads.cozystack.io/resource-preset": "large",
				"workloads.cozystack.io/tier":            "db",
			},
		},
		{
			name: "no matching labels returns empty map",
			labels: map[string]string{
				"app.kubernetes.io/name": "postgres",
			},
			expected: map[string]string{},
		},
	}

	r := &WorkloadMonitorReconciler{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			monitor := &cozyv1alpha1.WorkloadMonitor{
				ObjectMeta: metav1.ObjectMeta{Labels: tc.labels},
			}
			got := r.getMonitorLabels(monitor)
			if len(got) != len(tc.expected) {
				t.Fatalf("expected %d labels, got %d (%v)", len(tc.expected), len(got), got)
			}
			for k, v := range tc.expected {
				if gv, ok := got[k]; !ok || gv != v {
					t.Errorf("expected label %q=%q, got %q", k, v, gv)
				}
			}
		})
	}
}

func TestReconcile_MonitorLabelsPropagatedToPodWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = cosiv1alpha1.AddToScheme(scheme)

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-monitor",
			Namespace: "default",
			Labels: map[string]string{
				"workloads.cozystack.io/resource-preset": "medium",
				"app.kubernetes.io/name":                 "ignored-not-propagated",
			},
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector: map[string]string{"app": "test"},
			Kind:     "postgres",
			Type:     "postgres",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "default",
			Labels: map[string]string{
				"app":                    "test",
				"app.kubernetes.io/name": "pod-wins-on-conflict",
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, pod).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-monitor", Namespace: "default"}}
	if _, err := reconciler.Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	workload := &cozyv1alpha1.Workload{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "pod-test-pod-1", Namespace: "default"}, workload); err != nil {
		t.Fatalf("Failed to get Workload: %v", err)
	}

	if got := workload.Labels["workloads.cozystack.io/resource-preset"]; got != "medium" {
		t.Errorf("expected monitor label propagated, got %q", got)
	}
	// Non-workloads.cozystack.io monitor labels must not be copied
	if _, ok := workload.Labels["app.kubernetes.io/name"]; !ok {
		t.Error("expected pod label to be present on Workload")
	}
	// Source-object label takes precedence on conflict
	if got := workload.Labels["app.kubernetes.io/name"]; got != "pod-wins-on-conflict" {
		t.Errorf("expected pod label to win on conflict, got %q", got)
	}
	// Reserved monitor label is always set from the monitor name
	if got := workload.Labels["workloads.cozystack.io/monitor"]; got != "test-monitor" {
		t.Errorf("expected monitor-name label, got %q", got)
	}
}

func TestReconcile_BackwardCompat_NoMonitorLabels(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = cosiv1alpha1.AddToScheme(scheme)

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-monitor",
			Namespace: "default",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector: map[string]string{"app": "test"},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, pod).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-monitor", Namespace: "default"}}
	if _, err := reconciler.Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	workload := &cozyv1alpha1.Workload{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "pod-test-pod-1", Namespace: "default"}, workload); err != nil {
		t.Fatalf("Failed to get Workload: %v", err)
	}
	for k := range workload.Labels {
		if strings.HasPrefix(k, "workloads.cozystack.io/") && k != "workloads.cozystack.io/monitor" {
			t.Errorf("unexpected workload label present: %q", k)
		}
	}
}

func TestReconcile_OperationalTrue_WhenNoMinReplicas(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = cosiv1alpha1.AddToScheme(scheme)

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-monitor",
			Namespace: "default",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector: map[string]string{"app": "test"},
			// No MinReplicas — should default to operational=true
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-monitor", Namespace: "default"}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	updated := &cozyv1alpha1.WorkloadMonitor{}
	if err := fakeClient.Get(context.TODO(), req.NamespacedName, updated); err != nil {
		t.Fatalf("Failed to get updated WorkloadMonitor: %v", err)
	}

	if updated.Status.Operational == nil {
		t.Fatal("Expected Operational to be set, got nil")
	}
	if !*updated.Status.Operational {
		t.Error("Expected Operational=true (no MinReplicas constraint), got false")
	}
}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = cosiv1alpha1.AddToScheme(s)
	return s
}

func TestReconcileBucketClaimCreatesWorkload(t *testing.T) {
	s := newTestScheme()

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Kind: "bucket",
			Type: "s3",
			Selector: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
	}

	bc := &cosiv1alpha1.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
			Labels: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
		Spec: cosiv1alpha1.BucketClaimSpec{
			BucketClassName: "seaweedfs",
			Protocols:       []cosiv1alpha1.Protocol{cosiv1alpha1.ProtocolS3},
		},
		Status: cosiv1alpha1.BucketClaimStatus{
			BucketReady: true,
			BucketName:  "cosi-abc123",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(monitor, bc).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: s}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-bucket",
		Namespace: "tenant-demo",
	}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	workload := &cozyv1alpha1.Workload{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "bucket-my-bucket",
		Namespace: "tenant-demo",
	}, workload)
	if err != nil {
		t.Fatalf("expected Workload to be created, got error: %v", err)
	}

	if workload.Status.Kind != "bucket" {
		t.Errorf("expected Kind=bucket, got %q", workload.Status.Kind)
	}
	if workload.Status.Type != "s3" {
		t.Errorf("expected Type=s3, got %q", workload.Status.Type)
	}
	if !workload.Status.Operational {
		t.Error("expected Operational=true for ready BucketClaim")
	}
}

func TestReconcileBucketClaimNotReady(t *testing.T) {
	s := newTestScheme()

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Kind: "bucket",
			Type: "s3",
			Selector: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
	}

	bc := &cosiv1alpha1.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
			Labels: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
		Spec: cosiv1alpha1.BucketClaimSpec{
			BucketClassName: "seaweedfs",
			Protocols:       []cosiv1alpha1.Protocol{cosiv1alpha1.ProtocolS3},
		},
		Status: cosiv1alpha1.BucketClaimStatus{
			BucketReady: false,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(monitor, bc).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: s}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-bucket",
		Namespace: "tenant-demo",
	}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	workload := &cozyv1alpha1.Workload{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "bucket-my-bucket",
		Namespace: "tenant-demo",
	}, workload)
	if err != nil {
		t.Fatalf("expected Workload to be created, got error: %v", err)
	}

	if workload.Status.Operational {
		t.Error("expected Operational=false for not-ready BucketClaim")
	}
}

func TestReconcile_MonitorLabelsPropagatedToBucketClaimWorkload(t *testing.T) {
	s := newTestScheme()

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
			Labels: map[string]string{
				"workloads.cozystack.io/resource-preset": "medium",
				"app.kubernetes.io/name":                 "ignored-not-propagated",
			},
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Kind: "bucket",
			Type: "s3",
			Selector: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
	}

	bc := &cosiv1alpha1.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
			Labels: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
				"app.kubernetes.io/name":     "bucket-wins-on-conflict",
			},
		},
		Spec: cosiv1alpha1.BucketClaimSpec{
			BucketClassName: "seaweedfs",
			Protocols:       []cosiv1alpha1.Protocol{cosiv1alpha1.ProtocolS3},
		},
		Status: cosiv1alpha1.BucketClaimStatus{
			BucketReady: true,
			BucketName:  "cosi-abc123",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(monitor, bc).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: s}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-bucket",
		Namespace: "tenant-demo",
	}}
	if _, err := reconciler.Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	workload := &cozyv1alpha1.Workload{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "bucket-my-bucket",
		Namespace: "tenant-demo",
	}, workload); err != nil {
		t.Fatalf("Failed to get Workload: %v", err)
	}

	if got := workload.Labels["workloads.cozystack.io/resource-preset"]; got != "medium" {
		t.Errorf("expected monitor label propagated, got %q", got)
	}
	// Source-object label takes precedence on conflict
	if got := workload.Labels["app.kubernetes.io/name"]; got != "bucket-wins-on-conflict" {
		t.Errorf("expected bucket claim label to win on conflict, got %q", got)
	}
	// Reserved monitor label is always set from the monitor name
	if got := workload.Labels["workloads.cozystack.io/monitor"]; got != "my-bucket" {
		t.Errorf("expected monitor-name label, got %q", got)
	}
}

func TestReconcileNoBucketClaimSkips(t *testing.T) {
	s := newTestScheme()

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-postgres",
			Namespace: "tenant-demo",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Kind: "postgres",
			Type: "postgres",
			Selector: map[string]string{
				"app.kubernetes.io/instance": "my-postgres",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: s}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-postgres",
		Namespace: "tenant-demo",
	}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	workloadList := &cozyv1alpha1.WorkloadList{}
	err = fakeClient.List(context.TODO(), workloadList)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	for _, w := range workloadList.Items {
		if w.Status.Kind == "bucket" {
			t.Error("expected no bucket workloads to be created for postgres monitor")
		}
	}
}

func TestQueryAllBucketMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"__name__":"SeaweedFS_s3_bucket_size_bytes","bucket":"bucket-aaa"},"value":[1713000000,"10485864"]},
			{"metric":{"__name__":"SeaweedFS_s3_bucket_physical_size_bytes","bucket":"bucket-aaa"},"value":[1713000000,"20971728"]},
			{"metric":{"__name__":"SeaweedFS_s3_bucket_size_bytes","bucket":"bucket-bbb"},"value":[1713000000,"0"]}
		]}}`)
	}))
	defer srv.Close()

	reconciler := &WorkloadMonitorReconciler{}
	metrics, err := reconciler.queryAllBucketMetrics(context.TODO(), srv.URL, []string{"bucket-aaa", "bucket-bbb"})
	if err != nil {
		t.Fatalf("queryAllBucketMetrics returned error: %v", err)
	}

	bm, ok := metrics["bucket-aaa"]
	if !ok {
		t.Fatal("expected bucket-aaa in metrics")
	}
	if !bm.HasLogical || bm.LogicalSize != 10485864 {
		t.Errorf("expected logical=10485864, got %d", bm.LogicalSize)
	}
	if !bm.HasPhysical || bm.PhysicalSize != 20971728 {
		t.Errorf("expected physical=20971728, got %d", bm.PhysicalSize)
	}

	bm2, ok := metrics["bucket-bbb"]
	if !ok {
		t.Fatal("expected bucket-bbb in metrics")
	}
	if !bm2.HasLogical || bm2.LogicalSize != 0 {
		t.Errorf("expected logical=0 for empty bucket, got %d", bm2.LogicalSize)
	}
	if bm2.HasPhysical {
		t.Error("expected no physical size for bucket-bbb")
	}
}

func TestQueryAllBucketMetricsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv.Close()

	reconciler := &WorkloadMonitorReconciler{}
	metrics, err := reconciler.queryAllBucketMetrics(context.TODO(), srv.URL, []string{"bucket-aaa", "bucket-bbb"})
	if err != nil {
		t.Fatalf("queryAllBucketMetrics returned error: %v", err)
	}
	if len(metrics) != 0 {
		t.Errorf("expected empty metrics, got %d", len(metrics))
	}
}

func TestQueryAllBucketMetricsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	reconciler := &WorkloadMonitorReconciler{}
	_, err := reconciler.queryAllBucketMetrics(context.TODO(), srv.URL, []string{"bucket-aaa", "bucket-bbb"})
	if err == nil {
		t.Error("expected error on server failure, got nil")
	}
}

func TestQueryAllBucketMetricsNoURL(t *testing.T) {
	reconciler := &WorkloadMonitorReconciler{}
	metrics, err := reconciler.queryAllBucketMetrics(context.TODO(), "", nil)
	if err != nil {
		t.Fatalf("queryAllBucketMetrics returned error: %v", err)
	}
	if len(metrics) != 0 {
		t.Errorf("expected empty metrics when URL is empty, got %d", len(metrics))
	}
}

func TestQueryAllBucketMetricsPathPrefixAndUserinfo(t *testing.T) {
	var gotPath, gotUser, gotPass string
	var gotOK bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotPass, gotOK = r.BasicAuth()
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv.Close()

	reconciler := &WorkloadMonitorReconciler{}
	// Trailing slash on the base URL must not produce a double slash before
	// /api/v1/query, and userinfo must arrive as basic auth.
	baseURL := strings.Replace(srv.URL, "http://", "http://billing:hunter2@", 1) + "/path/to/prometheus/"
	if _, err := reconciler.queryAllBucketMetrics(context.TODO(), baseURL, []string{"bucket-aaa"}); err != nil {
		t.Fatalf("queryAllBucketMetrics returned error: %v", err)
	}

	if gotPath != "/path/to/prometheus/api/v1/query" {
		t.Errorf("expected path prefix preserved, got %q", gotPath)
	}
	if !gotOK || gotUser != "billing" || gotPass != "hunter2" {
		t.Errorf("expected basic auth billing/hunter2 from userinfo, got %q/%q ok=%v", gotUser, gotPass, gotOK)
	}
}

func TestParseMetricsEndpointURL(t *testing.T) {
	valid := map[string]string{
		"https://vm.example.com":                       "https://vm.example.com",
		"https://vm.example.com/":                      "https://vm.example.com",
		"https://vm.example.com:8427/vm/prometheus":    "https://vm.example.com:8427/vm/prometheus",
		"https://user:pass@vm.example.com/prometheus":  "https://user:pass@vm.example.com/prometheus",
		"http://vm.example.com/select/0/prometheus///": "http://vm.example.com/select/0/prometheus",
	}
	for raw, want := range valid {
		got, err := ParseMetricsEndpointURL(raw)
		if err != nil {
			t.Errorf("ParseMetricsEndpointURL(%q) returned error: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMetricsEndpointURL(%q) = %q, want %q", raw, got, want)
		}
	}

	invalid := []string{
		"",
		"vm.example.com/prometheus",
		"ftp://vm.example.com",
		"https://",
		"https://vm.example.com/prometheus?query=up",
		"https://vm.example.com/prometheus#frag",
	}
	for _, raw := range invalid {
		if got, err := ParseMetricsEndpointURL(raw); err == nil {
			t.Errorf("ParseMetricsEndpointURL(%q) = %q, want error", raw, got)
		}
	}
}

func TestResolvePrometheusURLFromLabel(t *testing.T) {
	s := newTestScheme()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-demo",
			Labels: map[string]string{
				"namespace.cozystack.io/monitoring": "tenant-root",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ns).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: s}
	url, err := reconciler.resolvePrometheusURL(context.TODO(), "tenant-demo")
	if err != nil {
		t.Fatalf("resolvePrometheusURL returned error: %v", err)
	}

	expected := "http://vmselect-shortterm.tenant-root.svc:8481/select/0/prometheus"
	if url != expected {
		t.Errorf("expected %q, got %q", expected, url)
	}
}

func TestResolvePrometheusURLNoLabel(t *testing.T) {
	s := newTestScheme()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-demo",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ns).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: s}
	url, err := reconciler.resolvePrometheusURL(context.TODO(), "tenant-demo")
	if err != nil {
		t.Fatalf("resolvePrometheusURL returned error: %v", err)
	}

	if url != "" {
		t.Errorf("expected empty URL when no monitoring label, got %q", url)
	}
}

func TestResolvePrometheusURLEndpointOverrideWinsOverLabel(t *testing.T) {
	s := newTestScheme()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-demo",
			Labels: map[string]string{
				"namespace.cozystack.io/monitoring": "tenant-root",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ns).
		Build()

	reconciler := &WorkloadMonitorReconciler{
		Client:                   fakeClient,
		Scheme:                   s,
		SeaweedfsMetricsEndpoint: "https://vm.example.com/path/to/prometheus",
	}
	url, err := reconciler.resolvePrometheusURL(context.TODO(), "tenant-demo")
	if err != nil {
		t.Fatalf("resolvePrometheusURL returned error: %v", err)
	}

	if url != "https://vm.example.com/path/to/prometheus" {
		t.Errorf("expected the endpoint override to win over label discovery, got %q", url)
	}
}

func TestReconcileBucketClaimRequeuesWhenBucketsExist(t *testing.T) {
	s := newTestScheme()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-demo",
		},
	}

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Kind: "bucket",
			Type: "s3",
			Selector: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
	}

	bc := &cosiv1alpha1.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
			Labels: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
		Spec: cosiv1alpha1.BucketClaimSpec{
			BucketClassName: "seaweedfs",
			Protocols:       []cosiv1alpha1.Protocol{cosiv1alpha1.ProtocolS3},
		},
		Status: cosiv1alpha1.BucketClaimStatus{
			BucketReady: true,
			BucketName:  "cosi-abc123",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ns, monitor, bc).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: s}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-bucket",
		Namespace: "tenant-demo",
	}}

	result, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 when buckets exist")
	}

	workload := &cozyv1alpha1.Workload{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "bucket-my-bucket",
		Namespace: "tenant-demo",
	}, workload)
	if err != nil {
		t.Fatalf("expected Workload to be created, got error: %v", err)
	}

	// Without monitoring label on namespace, no size metrics should be set
	if _, ok := workload.Status.Resources["s3-storage-bytes"]; ok {
		t.Error("expected no s3-storage-bytes when monitoring is not configured")
	}
	if len(workload.Status.Resources) != 0 {
		t.Errorf("expected empty resources without monitoring, got %v", workload.Status.Resources)
	}
}

// newBucketFixture returns a plain namespace, a bucket WorkloadMonitor, and a
// ready BucketClaim named my-bucket whose SeaweedFS bucket is cosi-abc123.
func newBucketFixture() (*corev1.Namespace, *cozyv1alpha1.WorkloadMonitor, *cosiv1alpha1.BucketClaim) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-demo",
		},
	}
	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Kind: "bucket",
			Type: "s3",
			Selector: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
	}
	bc := &cosiv1alpha1.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "tenant-demo",
			Labels: map[string]string{
				"app.kubernetes.io/instance": "my-bucket",
			},
		},
		Spec: cosiv1alpha1.BucketClaimSpec{
			BucketClassName: "seaweedfs",
			Protocols:       []cosiv1alpha1.Protocol{cosiv1alpha1.ProtocolS3},
		},
		Status: cosiv1alpha1.BucketClaimStatus{
			BucketReady: true,
			BucketName:  "cosi-abc123",
		},
	}
	return ns, monitor, bc
}

func TestReconcileBucketMetricsFromEndpointOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vm/select/0/prometheus/api/v1/query" {
			t.Errorf("unexpected query path %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"__name__":"SeaweedFS_s3_bucket_size_bytes","bucket":"cosi-abc123"},"value":[1713000000,"4096"]}
		]}}`)
	}))
	defer srv.Close()

	s := newTestScheme()
	ns, monitor, bc := newBucketFixture()

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ns, monitor, bc).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{
		Client:                   fakeClient,
		Scheme:                   s,
		SeaweedfsMetricsEndpoint: srv.URL + "/vm/select/0/prometheus",
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-bucket",
		Namespace: "tenant-demo",
	}}
	if _, err := reconciler.Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	workload := &cozyv1alpha1.Workload{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "bucket-my-bucket",
		Namespace: "tenant-demo",
	}, workload); err != nil {
		t.Fatalf("Failed to get Workload: %v", err)
	}

	q, ok := workload.Status.Resources["s3-storage-bytes"]
	if !ok {
		t.Fatal("expected s3-storage-bytes from the overridden endpoint")
	}
	if q.Value() != 4096 {
		t.Errorf("expected s3-storage-bytes=4096, got %d", q.Value())
	}
}

func TestReconcileRetainsLastKnownSizesOnQueryFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	s := newTestScheme()
	ns, monitor, bc := newBucketFixture()

	// A Workload from a previous successful reconcile already carries sizes.
	workload := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bucket-my-bucket",
			Namespace: "tenant-demo",
		},
		Status: cozyv1alpha1.WorkloadStatus{
			Kind: "bucket",
			Type: "s3",
			Resources: map[string]resource.Quantity{
				"s3-storage-bytes":          *resource.NewQuantity(4096, resource.BinarySI),
				"s3-physical-storage-bytes": *resource.NewQuantity(8192, resource.BinarySI),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ns, monitor, bc, workload).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{
		Client:                   fakeClient,
		Scheme:                   s,
		SeaweedfsMetricsEndpoint: srv.URL,
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-bucket",
		Namespace: "tenant-demo",
	}}

	// The failure must be loud: the reconcile returns an error instead of
	// pretending the buckets are empty.
	if _, err := reconciler.Reconcile(context.TODO(), req); err == nil {
		t.Error("expected Reconcile to return an error when the metrics endpoint fails")
	}

	updated := &cozyv1alpha1.Workload{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "bucket-my-bucket",
		Namespace: "tenant-demo",
	}, updated); err != nil {
		t.Fatalf("Failed to get Workload: %v", err)
	}

	q, ok := updated.Status.Resources["s3-storage-bytes"]
	if !ok || q.Value() != 4096 {
		t.Errorf("expected last known s3-storage-bytes=4096 retained, got %v (present=%v)", q.Value(), ok)
	}
	qp, ok := updated.Status.Resources["s3-physical-storage-bytes"]
	if !ok || qp.Value() != 8192 {
		t.Errorf("expected last known s3-physical-storage-bytes=8192 retained, got %v (present=%v)", qp.Value(), ok)
	}
}

func TestReconcileRetainsLastKnownSizesWhenBucketMissingFromResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv.Close()

	s := newTestScheme()
	ns, monitor, bc := newBucketFixture()

	workload := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bucket-my-bucket",
			Namespace: "tenant-demo",
		},
		Status: cozyv1alpha1.WorkloadStatus{
			Kind: "bucket",
			Type: "s3",
			Resources: map[string]resource.Quantity{
				"s3-storage-bytes": *resource.NewQuantity(4096, resource.BinarySI),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ns, monitor, bc, workload).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{
		Client:                   fakeClient,
		Scheme:                   s,
		SeaweedfsMetricsEndpoint: srv.URL,
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-bucket",
		Namespace: "tenant-demo",
	}}
	if _, err := reconciler.Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	updated := &cozyv1alpha1.Workload{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "bucket-my-bucket",
		Namespace: "tenant-demo",
	}, updated); err != nil {
		t.Fatalf("Failed to get Workload: %v", err)
	}

	q, ok := updated.Status.Resources["s3-storage-bytes"]
	if !ok || q.Value() != 4096 {
		t.Errorf("expected last known s3-storage-bytes=4096 retained when series is absent, got %v (present=%v)", q.Value(), ok)
	}
}
