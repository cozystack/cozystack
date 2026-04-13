package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	qty, ok := workload.Status.Resources["s3-buckets"]
	if !ok {
		t.Fatal("expected s3-buckets resource to be set")
	}
	if qty.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("expected s3-buckets=1, got %s", qty.String())
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

func TestQueryPrometheusMetric(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"bucket":"cosi-abc123"},"value":[1713000000,"5368709120"]}]}}`)
	}))
	defer srv.Close()

	reconciler := &WorkloadMonitorReconciler{PrometheusURL: srv.URL}
	size := reconciler.queryPrometheusMetric(context.TODO(), `SeaweedFS_s3_bucket_size_bytes{bucket="cosi-abc123"}`)
	if size != 5368709120 {
		t.Errorf("expected 5368709120, got %d", size)
	}
}

func TestQueryPrometheusMetricEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv.Close()

	reconciler := &WorkloadMonitorReconciler{PrometheusURL: srv.URL}
	size := reconciler.queryPrometheusMetric(context.TODO(), `SeaweedFS_s3_bucket_size_bytes{bucket="nonexistent"}`)
	if size != 0 {
		t.Errorf("expected 0 for empty result, got %d", size)
	}
}

func TestQueryPrometheusMetricNoURL(t *testing.T) {
	reconciler := &WorkloadMonitorReconciler{PrometheusURL: ""}
	size := reconciler.queryPrometheusMetric(context.TODO(), `anything`)
	if size != 0 {
		t.Errorf("expected 0 when PrometheusURL is empty, got %d", size)
	}
}

func TestReconcileBucketClaimWithPrometheus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		switch {
		case len(query) > 0 && query[0:len("SeaweedFS_s3_bucket_physical")] == "SeaweedFS_s3_bucket_physical":
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"bucket":"cosi-abc123"},"value":[1713000000,"2147483648"]}]}}`)
		default:
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"bucket":"cosi-abc123"},"value":[1713000000,"1073741824"]}]}}`)
		}
	}))
	defer srv.Close()

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

	reconciler := &WorkloadMonitorReconciler{
		Client:        fakeClient,
		Scheme:        s,
		PrometheusURL: srv.URL,
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      "my-bucket",
		Namespace: "tenant-demo",
	}}

	result, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 when PrometheusURL is set and buckets exist")
	}

	workload := &cozyv1alpha1.Workload{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "bucket-my-bucket",
		Namespace: "tenant-demo",
	}, workload)
	if err != nil {
		t.Fatalf("expected Workload to be created, got error: %v", err)
	}

	sizeQty, ok := workload.Status.Resources["s3-storage-bytes"]
	if !ok {
		t.Fatal("expected s3-storage-bytes resource to be set")
	}
	if sizeQty.Value() != 1073741824 {
		t.Errorf("expected s3-storage-bytes=1073741824 (1 GiB), got %d", sizeQty.Value())
	}

	bucketsQty, ok := workload.Status.Resources["s3-buckets"]
	if !ok {
		t.Fatal("expected s3-buckets resource to be set")
	}
	if bucketsQty.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("expected s3-buckets=1, got %s", bucketsQty.String())
	}

	physQty, ok := workload.Status.Resources["s3-physical-storage-bytes"]
	if !ok {
		t.Fatal("expected s3-physical-storage-bytes resource to be set")
	}
	if physQty.Value() != 2147483648 {
		t.Errorf("expected s3-physical-storage-bytes=2147483648 (2 GiB), got %d", physQty.Value())
	}
}
