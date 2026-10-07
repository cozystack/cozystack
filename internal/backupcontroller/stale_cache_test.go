// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

// staleCacheScheme registers what both the cache and the apiserver stand-in
// hold.
func staleCacheScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = velerov1.AddToScheme(s)
	return s
}

// apiServerHolding stands in for the apiserver: it holds job as the cache
// does, except for its phase.
func apiServerHolding(s *runtime.Scheme, job client.Object, phase string) client.Reader {
	onAPI := job.DeepCopyObject().(client.Object)
	switch j := onAPI.(type) {
	case *backupsv1alpha1.BackupJob:
		j.Status.Phase = backupsv1alpha1.BackupJobPhase(phase)
	case *backupsv1alpha1.RestoreJob:
		j.Status.Phase = backupsv1alpha1.RestoreJobPhase(phase)
	}
	onAPI.SetResourceVersion("")
	return clientfake.NewClientBuilder().WithScheme(s).WithObjects(onAPI).Build()
}

func countIn(t *testing.T, c client.Client, list client.ObjectList) int {
	t.Helper()
	if err := c.List(context.Background(), list); err != nil {
		t.Fatalf("list %T: %v", list, err)
	}
	return meta.LenList(list)
}

// TestReconcile_DoesNotRetryAJobItAlreadyFailed reproduces a pass that
// starts on a cache trailing the controller's own writes: the previous pass
// stamped StartedAt, which requeued the job, then failed it on a transient
// error. The cache holds the job with StartedAt and no phase; the apiserver
// holds it Failed. Before the re-read, the pass retried the Velero Backup
// creation and the job ended Failed with a Velero Backup nothing tracks.
func TestReconcile_DoesNotRetryAJobItAlreadyFailed(t *testing.T) {
	appsGroup := "apps.cozystack.io"
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	started := metav1.NewTime(time.Now().Add(-time.Second).Truncate(time.Second))

	run := func(t *testing.T, apiPhase backupsv1alpha1.BackupJobPhase) (created int, cached backupsv1alpha1.BackupJobPhase) {
		s := staleCacheScheme()
		bc := &backupsv1alpha1.BackupClass{
			ObjectMeta: metav1.ObjectMeta{Name: "bc"},
			Spec: backupsv1alpha1.BackupClassSpec{Strategies: []backupsv1alpha1.BackupClassStrategy{{
				Application: backupsv1alpha1.ApplicationSelector{APIGroup: &appsGroup, Kind: "Bucket"},
				StrategyRef: corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.VeleroStrategyKind, Name: "velero"},
			}}},
		}
		j := &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
			Spec: backupsv1alpha1.BackupJobSpec{
				BackupClassName: "bc",
				ApplicationRef:  corev1.TypedLocalObjectReference{APIGroup: &appsGroup, Kind: "Bucket", Name: "app"},
			},
			Status: backupsv1alpha1.BackupJobStatus{StartedAt: &started},
		}
		strategy := &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "velero"}}
		c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(j, bc, strategy).
			WithStatusSubresource(&backupsv1alpha1.BackupJob{}).Build()
		gvr := schema.GroupVersionResource{Group: appsGroup, Version: "v1alpha1", Resource: "buckets"}
		app := &unstructured.Unstructured{}
		app.SetGroupVersionKind(gvr.GroupVersion().WithKind("Bucket"))
		app.SetNamespace("tenant")
		app.SetName("app")
		r := &BackupJobReconciler{
			Client:    c,
			APIReader: apiServerHolding(s, j, string(apiPhase)),
			Interface: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "BucketList"}, app),
			RESTMapper: &mockRESTMapper{mapping: &meta.RESTMapping{
				Resource:         gvr,
				GroupVersionKind: gvr.GroupVersion().WithKind("Bucket"),
				Scope:            meta.RESTScopeNamespace,
			}},
			Scheme:   s,
			Recorder: record.NewFakeRecorder(10),
		}
		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(j)}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		got := &backupsv1alpha1.BackupJob{}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(j), got); err != nil {
			t.Fatalf("get BackupJob: %v", err)
		}
		return countIn(t, c, &velerov1.BackupList{}), got.Status.Phase
	}

	if created, _ := run(t, ""); created != 1 {
		t.Fatalf("job still active on the apiserver: %d Velero Backups created, want 1", created)
	}
	created, cached := run(t, backupsv1alpha1.BackupJobPhaseFailed)
	if created != 0 {
		t.Errorf("job Failed on the apiserver: %d Velero Backups created, want 0", created)
	}
	if cached != "" {
		t.Errorf("the pass wrote phase %q over a job it should have left alone", cached)
	}
}

// TestReconcileRestore_DoesNotRetryAJobItAlreadyFailed is the restore-side
// counterpart. The retried step is destructive there: before creating the
// Velero Restore, the pass suspends the VM's HelmRelease and halts the VM.
func TestReconcileRestore_DoesNotRetryAJobItAlreadyFailed(t *testing.T) {
	const ns = "tenant"
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	hrName := vmNamePrefix + "vm"
	started := metav1.NewTime(time.Now().Add(-time.Second).Truncate(time.Second))

	run := func(t *testing.T, apiPhase backupsv1alpha1.RestoreJobPhase) (restores int, hrSuspended bool, cached backupsv1alpha1.RestoreJobPhase) {
		s := staleCacheScheme()
		backup := &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
			Spec: backupsv1alpha1.BackupSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: vmInstanceKind, Name: "vm"},
				StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.VeleroStrategyKind, Name: "velero"},
				DriverMetadata: map[string]string{veleroBackupNameMetadataKey: "vb"},
			},
		}
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns, Finalizers: []string{restoreJobFinalizer}},
			Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
			Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &started, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
		}
		strategy := &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "velero"}}
		c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(rj, backup, strategy).
			WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).Build()
		dyn := dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t), makeUnstructuredHelmRelease(hrName, ns, nil))
		r := &RestoreJobReconciler{
			Client:    c,
			APIReader: apiServerHolding(s, rj, string(apiPhase)),
			Interface: dyn,
			Scheme:    s,
			Recorder:  record.NewFakeRecorder(100),
		}
		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(rj)}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		hr, err := dyn.Resource(helmReleaseGVR).Namespace(ns).Get(context.Background(), hrName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get HelmRelease: %v", err)
		}
		hrSuspended, _, _ = unstructured.NestedBool(hr.Object, "spec", "suspend")
		got := &backupsv1alpha1.RestoreJob{}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
			t.Fatalf("get RestoreJob: %v", err)
		}
		return countIn(t, c, &velerov1.RestoreList{}), hrSuspended, got.Status.Phase
	}

	if restores, suspended, _ := run(t, backupsv1alpha1.RestoreJobPhaseRunning); restores != 1 || !suspended {
		t.Fatalf("job still active on the apiserver: %d Velero Restores, HelmRelease suspended=%v; want 1 and true", restores, suspended)
	}
	restores, suspended, cached := run(t, backupsv1alpha1.RestoreJobPhaseFailed)
	if suspended {
		t.Error("job Failed on the apiserver: the pass suspended the VM's HelmRelease")
	}
	if restores != 0 {
		t.Errorf("job Failed on the apiserver: %d Velero Restores created, want 0", restores)
	}
	if cached != backupsv1alpha1.RestoreJobPhaseRunning {
		t.Errorf("the pass wrote phase %q over a job it should have left alone", cached)
	}
}
