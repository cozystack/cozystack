// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// newAltinityClickHouseApp returns an unstructured ClickHouse app the
// driver's dynamic client can serve. The fake client matches GVR exactly,
// so the apiVersion/kind/group must align with what mockRESTMapper returns
// below.
func newAltinityClickHouseApp(name, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   backupsv1alpha1.DefaultApplicationAPIGroup,
		Version: "v1alpha1",
		Kind:    "ClickHouse",
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

// altinityControllerRef is the controllerRef ensureAltinityJob gives the
// backup Job it creates for j.
func altinityControllerRef(j *backupsv1alpha1.BackupJob) []metav1.OwnerReference {
	return []metav1.OwnerReference{*metav1.NewControllerRef(j, backupsv1alpha1.GroupVersion.WithKind("BackupJob"))}
}

// newAltinityTestEnv builds the scheme, dynamic client, REST mapper, and
// reconciler harness used across Altinity strategy unit tests.
func newAltinityTestEnv(t *testing.T, app *unstructured.Unstructured, builder *clientfake.ClientBuilder) (*BackupJobReconciler, *RestoreJobReconciler, *runtime.Scheme) {
	t.Helper()

	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)

	gvr := schema.GroupVersionResource{
		Group:    backupsv1alpha1.DefaultApplicationAPIGroup,
		Version:  "v1alpha1",
		Resource: "clickhouses",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		testScheme,
		map[schema.GroupVersionResource]string{gvr: "ClickHouseList"},
		app,
	)
	mapping := &meta.RESTMapping{
		Resource:         gvr,
		GroupVersionKind: app.GroupVersionKind(),
		Scope:            meta.RESTScopeNamespace,
	}
	restMapper := &mockRESTMapper{mapping: mapping}

	c := builder.WithScheme(testScheme).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()

	return &BackupJobReconciler{
		Client:     c,
		Interface:  dynamicClient,
		RESTMapper: restMapper,
		Scheme:     testScheme,
		Recorder:   record.NewFakeRecorder(10),
	}, &RestoreJobReconciler{
		Client:     c,
		Interface:  dynamicClient,
		RESTMapper: restMapper,
		Scheme:     testScheme,
		Recorder:   record.NewFakeRecorder(10),
	}, testScheme
}

func newAltinityStrategy(name string) *strategyv1alpha1.Altinity {
	return &strategyv1alpha1.Altinity{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: strategyv1alpha1.AltinitySpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "clickhouse-backup",
						Image: "altinity/clickhouse-backup:test",
						Args: []string{
							"--release={{ .Release.Name }}",
							"--mode={{ .Mode }}",
							"--bucket={{ .Parameters.bucketName }}",
						},
					}},
				},
			},
		},
	}
}

func newAltinityClickHouseRef(name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{
		APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup),
		Kind:     "ClickHouse",
		Name:     name,
	}
}

func TestReconcileAltinity_CreatesBatchJob(t *testing.T) {
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	strategy := newAltinityStrategy("clickhouse-strategy")

	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
	}

	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
		Parameters: map[string]string{"bucketName": "ch-bucket"},
	}

	r, _, _ := newAltinityTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	ctx := context.Background()

	// First reconcile patches Status.StartedAt and requeues without
	// touching the Job - mirrors the CNPG/Velero pattern that avoids
	// carrying a stale ResourceVersion into the next status write in the
	// same reconcile.
	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() first call: %v", err)
	}
	// Refresh local copy and reconcile again - this is the call that
	// materialises the Job once StartedAt is already set.
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), backupJob); err != nil {
		t.Fatalf("refresh BackupJob after first reconcile: %v", err)
	}
	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() second call: %v", err)
	}

	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 batch/v1.Job, got %d", len(jobs.Items))
	}
	k8sJob := jobs.Items[0]
	if got := k8sJob.Labels[backupsv1alpha1.OwningJobNameLabel]; got != backupJob.Name {
		t.Errorf("expected owning label %q, got %q", backupJob.Name, got)
	}
	if got := k8sJob.Labels[altinityLabelMode]; got != altinityModeBackup {
		t.Errorf("expected mode label %q, got %q", altinityModeBackup, got)
	}

	containerArgs := k8sJob.Spec.Template.Spec.Containers[0].Args
	wantArgs := []string{"--release=ch-test", "--mode=backup", "--bucket=ch-bucket"}
	for i, want := range wantArgs {
		if i >= len(containerArgs) || containerArgs[i] != want {
			t.Errorf("rendered args[%d]: want %q, got %q", i, want, containerArgs)
			break
		}
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: backupJob.Namespace, Name: backupJob.Name}, updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		t.Errorf("expected phase Running after first reconcile, got %q", updated.Status.Phase)
	}
	if updated.Status.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
}

func TestReconcileAltinity_CompletesAndCreatesBackup(t *testing.T) {
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	strategy := newAltinityStrategy("clickhouse-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{
			StartedAt: &now,
			Phase:     backupsv1alpha1.BackupJobPhaseRunning,
		},
	}
	completedK8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobNameForBackupJob(backupJob),
			Namespace:       backupJob.Namespace,
			OwnerReferences: altinityControllerRef(backupJob),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      backupJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: backupJob.Namespace,
			},
		},
		Status: batchv1.JobStatus{
			CompletionTime: &now,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}

	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
		Parameters: map[string]string{"bucketName": "ch-bucket"},
	}

	r, _, _ := newAltinityTestEnv(t, app,
		clientfake.NewClientBuilder().WithObjects(backupJob, strategy, completedK8sJob))
	ctx := context.Background()

	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() error = %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: backupJob.Namespace, Name: backupJob.Name}, updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseSucceeded {
		t.Errorf("expected phase Succeeded, got %q", updated.Status.Phase)
	}
	if updated.Status.BackupRef == nil || updated.Status.BackupRef.Name == "" {
		t.Fatalf("expected BackupRef to be set, got %v", updated.Status.BackupRef)
	}
	if updated.Status.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}

	created := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: backupJob.Namespace, Name: updated.Status.BackupRef.Name}, created); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	if created.Spec.StrategyRef.Name != "clickhouse-strategy" {
		t.Errorf("expected StrategyRef name 'clickhouse-strategy', got %q", created.Spec.StrategyRef.Name)
	}
	if got := created.Spec.DriverMetadata[altinityParamPrefix+"bucketName"]; got != "ch-bucket" {
		t.Errorf("expected driverMetadata[parameter/bucketName]=ch-bucket, got %q", got)
	}
	// Status.Phase is intentionally NOT asserted here. The driver creates
	// the Backup with Phase=Ready in its in-memory struct, but Backup has
	// the status subresource enabled so the apiserver drops status on
	// Create. The fake client's WithStatusSubresource registration mirrors
	// that behaviour, and this driver does not own status promotion - that
	// is the BackupReconciler's job (see internal/backupcontroller/
	// backup_controller.go). Asserting Ready here would test the in-memory
	// view of the create payload instead of the contract this driver
	// publishes.
}

func TestReconcileAltinity_FailsOnJobFailed(t *testing.T) {
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	strategy := newAltinityStrategy("clickhouse-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	failedK8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobNameForBackupJob(backupJob),
			Namespace:       backupJob.Namespace,
			OwnerReferences: altinityControllerRef(backupJob),
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      backupJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: backupJob.Namespace,
			},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "BackoffLimitExceeded"},
			},
		},
	}

	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
	}

	r, _, _ := newAltinityTestEnv(t, app,
		clientfake.NewClientBuilder().WithObjects(backupJob, strategy, failedK8sJob))
	ctx := context.Background()

	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() error = %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: backupJob.Namespace, Name: backupJob.Name}, updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed, got %q", updated.Status.Phase)
	}
	if updated.Status.Message == "" {
		t.Error("expected failure message to be set")
	}
}

func TestReconcileAltinityRestore_CreatesBatchJobInTargetNamespace(t *testing.T) {
	targetApp := newAltinityClickHouseApp("ch-restore", "tenant-test")
	strategy := &strategyv1alpha1.Altinity{
		ObjectMeta: metav1.ObjectMeta{Name: "clickhouse-strategy"},
		Spec: strategyv1alpha1.AltinitySpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "clickhouse-backup",
						Image: "altinity/clickhouse-backup:test",
						Args: []string{
							"--release={{ .Release.Name }}",
							"--mode={{ .Mode }}",
							"--source={{ .Backup.Name }}",
						},
					}},
				},
			},
		},
	}
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newAltinityClickHouseRef("ch-test"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: new(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.AltinityStrategyKind,
				Name:     "clickhouse-strategy",
			},
			TakenAt:        now,
			DriverMetadata: map[string]string{altinityParamPrefix + "bucketName": "ch-bucket"},
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: backup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup),
				Kind:     "ClickHouse",
				Name:     "ch-restore",
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}

	_, rr, _ := newAltinityTestEnv(t, targetApp,
		clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
	ctx := context.Background()

	if _, err := rr.reconcileAltinityRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileAltinityRestore() error = %v", err)
	}

	jobs := &batchv1.JobList{}
	if err := rr.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 restore Job, got %d", len(jobs.Items))
	}
	k8sJob := jobs.Items[0]
	if got := k8sJob.Labels[altinityLabelMode]; got != altinityModeRestore {
		t.Errorf("expected mode label %q, got %q", altinityModeRestore, got)
	}
	args := k8sJob.Spec.Template.Spec.Containers[0].Args
	want := []string{"--release=ch-restore", "--mode=restore", "--source=ch-backup"}
	for i, w := range want {
		if i >= len(args) || args[i] != w {
			t.Errorf("rendered restore args[%d]: want %q, got %q", i, w, args)
		}
	}
}

// TestReconcileAltinity_RenderedBackupJobOwnedByBackupJob locks in review
// Blocker 3: the rendered batch/v1.Job must carry an OwnerReferences entry
// pointing at the BackupJob so kube-gc collects it (and the running Pod)
// when the BackupJob is deleted. Before the fix the Job had no
// controllerRef; tenants who deleted a BackupJob mid-run leaked the Job.
func TestReconcileAltinity_RenderedBackupJobOwnedByBackupJob(t *testing.T) {
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	strategy := newAltinityStrategy("clickhouse-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{
			StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning,
		},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
	}
	r, _, _ := newAltinityTestEnv(t, app,
		clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	ctx := context.Background()

	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity: %v", err)
	}

	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 backup Job, got %d", len(jobs.Items))
	}
	owners := jobs.Items[0].OwnerReferences
	if len(owners) == 0 {
		t.Fatal("rendered Job has no OwnerReferences (kube-gc would not collect on BackupJob delete)")
	}
	got := owners[0]
	if got.Kind != "BackupJob" || got.Name != backupJob.Name || got.UID != backupJob.UID {
		t.Errorf("OwnerRef does not point at BackupJob: %+v", got)
	}
	if got.Controller == nil || !*got.Controller {
		t.Error("OwnerRef.Controller must be true so kube-gc treats this as the controlling owner")
	}
}

// TestReconcileAltinityRestore_RenderedRestoreJobOwnedByRestoreJob mirrors
// the BackupJob test for the restore path: the rendered Job in the same
// namespace as the RestoreJob (the supported case - cross-namespace restore
// is rejected at the API level by TypedLocalObjectReference) must carry an
// OwnerReferences entry on the RestoreJob.
func TestReconcileAltinityRestore_RenderedRestoreJobOwnedByRestoreJob(t *testing.T) {
	targetApp := newAltinityClickHouseApp("ch-restore", "tenant-test")
	strategy := newAltinityStrategy("clickhouse-strategy")
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newAltinityClickHouseRef("ch-test"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: new(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.AltinityStrategyKind,
				Name:     "clickhouse-strategy",
			},
			TakenAt: now,
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: backup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup),
				Kind:     "ClickHouse",
				Name:     "ch-restore",
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	_, rr, _ := newAltinityTestEnv(t, targetApp,
		clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
	ctx := context.Background()

	if _, err := rr.reconcileAltinityRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileAltinityRestore: %v", err)
	}

	jobs := &batchv1.JobList{}
	if err := rr.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 restore Job, got %d", len(jobs.Items))
	}
	owners := jobs.Items[0].OwnerReferences
	if len(owners) == 0 {
		t.Fatal("rendered restore Job has no OwnerReferences")
	}
	got := owners[0]
	if got.Kind != "RestoreJob" || got.Name != restoreJob.Name || got.UID != restoreJob.UID {
		t.Errorf("OwnerRef does not point at RestoreJob: %+v", got)
	}
	if got.Controller == nil || !*got.Controller {
		t.Error("OwnerRef.Controller must be true")
	}
}

// TestReconcileAltinityRestore_TargetNamespaceIsRestoreJobNamespace pins
// the invariant that the rendered restore Job always lives in the
// RestoreJob's own namespace, regardless of TargetApplicationRef shape.
// TargetApplicationRef is corev1.TypedLocalObjectReference (no Namespace
// field) and BackupRef is corev1.LocalObjectReference, so cross-namespace
// restore is not representable in the current API. Locking this in
// catches regressions if a future API change reintroduces a Namespace
// field on either ref - the helper that owns the OwnerReference (and
// kube-gc collection) assumes same-namespace and would silently leak Jobs
// otherwise.
func TestReconcileAltinityRestore_TargetNamespaceIsRestoreJobNamespace(t *testing.T) {
	cases := []struct {
		name       string
		targetRef  *corev1.TypedLocalObjectReference
		targetName string // app the dynamic client serves
	}{
		{
			name:       "nil targetRef -> in-place restore",
			targetRef:  nil,
			targetName: "ch-source",
		},
		{
			name: "name-only targetRef -> to-copy with default APIGroup/Kind",
			targetRef: &corev1.TypedLocalObjectReference{
				Name: "ch-copy",
			},
			targetName: "ch-copy",
		},
		{
			name: "full triple targetRef -> to-copy explicit",
			targetRef: &corev1.TypedLocalObjectReference{
				APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup),
				Kind:     "ClickHouse",
				Name:     "ch-copy",
			},
			targetName: "ch-copy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targetApp := newAltinityClickHouseApp(tc.targetName, "tenant-foo")
			strategy := newAltinityStrategy("clickhouse-strategy")
			now := metav1.Now()
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "ch-backup", Namespace: "tenant-foo"},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: newAltinityClickHouseRef("ch-source"),
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: new(strategyv1alpha1.GroupVersion.Group),
						Kind:     strategyv1alpha1.AltinityStrategyKind,
						Name:     "clickhouse-strategy",
					},
					TakenAt: now,
				},
			}
			restoreJob := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-foo"},
				Spec: backupsv1alpha1.RestoreJobSpec{
					BackupRef:            corev1.LocalObjectReference{Name: backup.Name},
					TargetApplicationRef: tc.targetRef,
				},
				Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
			}
			_, rr, _ := newAltinityTestEnv(t, targetApp,
				clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
			ctx := context.Background()

			if _, err := rr.reconcileAltinityRestore(ctx, restoreJob, backup); err != nil {
				t.Fatalf("reconcileAltinityRestore: %v", err)
			}
			jobs := &batchv1.JobList{}
			if err := rr.List(ctx, jobs); err != nil {
				t.Fatalf("list batch jobs: %v", err)
			}
			if len(jobs.Items) != 1 {
				t.Fatalf("expected 1 restore Job, got %d", len(jobs.Items))
			}
			if got := jobs.Items[0].Namespace; got != "tenant-foo" {
				t.Errorf("rendered Job lives in %q, expected RestoreJob's namespace tenant-foo", got)
			}
			owners := jobs.Items[0].OwnerReferences
			if len(owners) == 0 || owners[0].Kind != "RestoreJob" || owners[0].Name != restoreJob.Name {
				t.Errorf("rendered Job missing controllerRef on RestoreJob: %+v", owners)
			}
		})
	}
}

// TestReconcileAltinityRestore_ExposesSourceApplicationRef pins the
// driver-side fix that lets restore strategies filter list/remote results
// by the *source* release name when restoring into a differently-named
// target (to-copy). Without `.Backup.ApplicationRef.Name` in the template
// context, the strategy script can only see `.Release.Name` (which is the
// destination) and any prefix-based filter against backup names ends up
// matching nothing - the script falls through to "no remote backup
// found" or, worse, blindly picks the latest archive in the bucket
// (regression covered by the multi-release isolation test in the bats
// e2e suite).
func TestReconcileAltinityRestore_ExposesSourceApplicationRef(t *testing.T) {
	targetApp := newAltinityClickHouseApp("ch-restore", "tenant-foo")
	strategy := &strategyv1alpha1.Altinity{
		ObjectMeta: metav1.ObjectMeta{Name: "clickhouse-strategy"},
		Spec: strategyv1alpha1.AltinitySpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "clickhouse-backup",
						Image: "altinity/clickhouse-backup:test",
						Args: []string{
							"--target={{ .Release.Name }}",
							"--source={{ .Backup.ApplicationRef.Name }}",
							"--source-kind={{ .Backup.ApplicationRef.Kind }}",
						},
					}},
				},
			},
		},
	}
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-backup", Namespace: "tenant-foo"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newAltinityClickHouseRef("ch-source"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: new(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.AltinityStrategyKind,
				Name:     "clickhouse-strategy",
			},
			TakenAt: now,
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-foo"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: backup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup),
				Kind:     "ClickHouse",
				Name:     "ch-restore",
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}

	_, rr, _ := newAltinityTestEnv(t, targetApp,
		clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
	ctx := context.Background()

	if _, err := rr.reconcileAltinityRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileAltinityRestore: %v", err)
	}

	jobs := &batchv1.JobList{}
	if err := rr.List(ctx, jobs, client.InNamespace("tenant-foo")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 restore Job, got %d", len(jobs.Items))
	}
	args := jobs.Items[0].Spec.Template.Spec.Containers[0].Args
	want := []string{"--target=ch-restore", "--source=ch-source", "--source-kind=ClickHouse"}
	for i, w := range want {
		if i >= len(args) || args[i] != w {
			t.Errorf("rendered restore args[%d]: want %q, got %q", i, w, args)
		}
	}
}

func TestBackupParameters_RoundTrip(t *testing.T) {
	backup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{
				altinityParamPrefix + "bucketName": "b1",
				altinityParamPrefix + "endpoint":   "https://s3",
				altinityArchiveMetaKey:             "clickhouse-ch-test-20260304T050607Z",
				"job.batch/name":                   "ignored",
				"":                                 "ignored",
			},
		},
	}
	got := backupParameters(backup)
	if got["bucketName"] != "b1" || got["endpoint"] != "https://s3" {
		t.Errorf("backupParameters = %v", got)
	}
	if len(got) != 2 {
		t.Errorf("non-parameter key leaked through: %v", got)
	}
}

// newAltinityArchiveStrategy renders the archive name and the Backup's
// takenAt where a test can read them back off the Job.
func newAltinityArchiveStrategy(name string) *strategyv1alpha1.Altinity {
	s := newAltinityStrategy(name)
	s.Spec.Template.Spec.Containers[0].Args = []string{
		"--mode={{ .Mode }}",
		"--archive={{ .ArchiveName }}",
		"--taken-at={{ .Backup.TakenAt }}",
		"--bucket={{ .Parameters.bucketName }}",
	}
	return s
}

func TestAltinityArchiveName_FromStartedAtInUTC(t *testing.T) {
	startedAt := metav1.NewTime(time.Date(2026, 3, 4, 8, 6, 7, 890000000, time.FixedZone("UTC+3", 3*3600)))
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{UID: "0f3c9a1e-5b7d-4c2a-9e8f-1a2b3c4d5e6f"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: newAltinityClickHouseRef("ch-test")},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt},
	}
	if got, want := altinityArchiveName(j), "ch-test-20260304050607-0f3c9a1e5b7d"; got != want {
		t.Errorf("altinityArchiveName = %q, want %q", got, want)
	}
}

// TestAltinityArchiveName_NoLongerThanTheNameWithoutUID pins the S3 key
// budget: the archive name is part of every object key, a key may not exceed
// 1024 bytes, and the legacy s3PathOverride prefix is unbounded, so a prefix
// that fit the name without the UID must still fit this one.
func TestAltinityArchiveName_NoLongerThanTheNameWithoutUID(t *testing.T) {
	startedAt := metav1.NewTime(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	for n := 1; n <= 63; n++ {
		j := &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{UID: "0f3c9a1e-5b7d-4c2a-9e8f-1a2b3c4d5e6f"},
			Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: newAltinityClickHouseRef(strings.Repeat("a", n))},
			Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt},
		}
		name, legacy := altinityArchiveName(j), altinityArchiveNameBeforeUIDs(j)
		if len(name) > len(legacy) {
			t.Errorf("application name of %d characters: archive %q is longer than %q", n, name, legacy)
		}
		// The manifest key of a prefix that left the name without the UID
		// exactly at the limit.
		prefix := strings.Repeat("p", 1024-len("/"+legacy+"/metadata.json"))
		if key := prefix + "/" + name + "/metadata.json"; len(key) > 1024 {
			t.Errorf("application name of %d characters: manifest key is %d bytes", n, len(key))
		}
	}
}

// TestAltinityArchiveName_DistinctForRunsStartedInTheSameSecond pins that two
// BackupJobs of one application started within the same second, as two Plans
// on the same schedule are, write different archives. With a shared name, the
// strategy takes the other run's finished upload for an earlier attempt of its
// own and records an archive missing what changed since that upload.
func TestAltinityArchiveName_DistinctForRunsStartedInTheSameSecond(t *testing.T) {
	startedAt := metav1.NewTime(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	run := func(name, uid string) *backupsv1alpha1.BackupJob {
		return &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-test", UID: types.UID(uid)},
			Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: newAltinityClickHouseRef("ch-test")},
			Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt},
		}
	}
	daily := altinityArchiveName(run("daily-1", "11111111-1111-1111-1111-111111111111"))
	weekly := altinityArchiveName(run("weekly-1", "22222222-2222-2222-2222-222222222222"))
	if daily == weekly {
		t.Fatalf("two runs started in the same second share archive %q", daily)
	}
	for _, name := range []string{daily, weekly} {
		if !strings.HasPrefix(name, "ch-test-20260304050607-") {
			t.Errorf("archive %q does not start with the application and start time, so names stop sorting chronologically", name)
		}
		if !altinityArchiveNamePattern.MatchString(name) {
			t.Errorf("archive %q is not a name the cleanup path accepts", name)
		}
	}
}

// TestReconcileAltinity_RecordsTheArchiveTheJobWasGiven drives one BackupJob
// through Job creation and completion in separate reconciles. The archive
// name has to come out of the persisted StartedAt both times: the Pod is
// told it on the first, the Backup records it on the second, and a restore
// later reads back exactly that archive.
func TestReconcileAltinity_RecordsTheArchiveTheJobWasGiven(t *testing.T) {
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	strategy := newAltinityArchiveStrategy("clickhouse-strategy")
	startedAt := metav1.NewTime(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test", UID: "test-bj-uid"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
		Parameters: map[string]string{"bucketName": "ch-bucket"},
	}
	r, _, _ := newAltinityTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	ctx := context.Background()
	const want = "ch-test-20260304050607-testbjuid"

	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() creating the Job: %v", err)
	}
	job := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: jobNameForBackupJob(backupJob)}, job); err != nil {
		t.Fatalf("get backup Job: %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Args[1]; got != "--archive="+want {
		t.Fatalf("backup Pod was rendered with %q, want --archive=%s", got, want)
	}

	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, job); err != nil {
		t.Fatalf("mark backup Job complete: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), backupJob); err != nil {
		t.Fatalf("refresh BackupJob: %v", err)
	}
	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() on the completed Job: %v", err)
	}

	created := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: backupJob.Name}, created); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	if got := created.Spec.DriverMetadata[altinityArchiveMetaKey]; got != want {
		t.Errorf("Backup records archive %q, want %q", got, want)
	}
	if got := created.Spec.DriverMetadata[altinityParamPrefix+"bucketName"]; got != "ch-bucket" {
		t.Errorf("expected driverMetadata[parameter/bucketName]=ch-bucket, got %q", got)
	}
}

// TestReconcileAltinity_RecordsTheArchiveOfAJobNamedBeforeUIDs covers a backup
// Job created by a controller that named archives without the BackupJob's UID
// and still running when the controller is upgraded: the Job is not
// re-rendered, so its archive carries the old name, and the Backup has to
// record that name rather than none.
func TestReconcileAltinity_RecordsTheArchiveOfAJobNamedBeforeUIDs(t *testing.T) {
	const legacy = "clickhouse-ch-test-20260304T050607Z"
	cases := []struct {
		name    string
		archive string // archive the Job's Pod was rendered with
		want    string
	}{
		{name: "the name without the UID", archive: legacy, want: legacy},
		{name: "a longer name that starts with it", archive: legacy + "-copy", want: ""},
		{name: "a longer name that ends with it", archive: "copy-" + legacy, want: ""},
		{name: "a longer name that continues it after a period", archive: legacy + ".copy", want: ""},
		{name: "a longer name that leads into it with a period", archive: "copy." + legacy, want: ""},
		{name: "a longer name that continues it after an underscore", archive: legacy + "_copy", want: ""},
		{name: "a longer name that leads into it with an underscore", archive: "copy_" + legacy, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordAltinityArchiveOfJobRenderedWith(t, tc.archive); got != tc.want {
				t.Errorf("Backup records archive %q, want %q", got, tc.want)
			}
		})
	}
}

// recordAltinityArchiveOfJobRenderedWith completes a backup Job whose Pod
// carries the given archive in place of the one the controller rendered, and
// returns the archive the resulting Backup records.
func recordAltinityArchiveOfJobRenderedWith(t *testing.T, archive string) string {
	t.Helper()
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	strategy := newAltinityArchiveStrategy("clickhouse-strategy")
	startedAt := metav1.NewTime(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test", UID: "test-bj-uid"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
	}
	r, _, _ := newAltinityTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	ctx := context.Background()

	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() creating the Job: %v", err)
	}
	job := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: jobNameForBackupJob(backupJob)}, job); err != nil {
		t.Fatalf("get backup Job: %v", err)
	}
	job.Spec.Template.Spec.Containers[0].Args[1] = "--archive=" + archive
	if err := r.Update(ctx, job); err != nil {
		t.Fatalf("render the backup Job with archive %q: %v", archive, err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, job); err != nil {
		t.Fatalf("mark backup Job complete: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), backupJob); err != nil {
		t.Fatalf("refresh BackupJob: %v", err)
	}
	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() on the completed Job: %v", err)
	}

	created := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: backupJob.Name}, created); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	return created.Spec.DriverMetadata[altinityArchiveMetaKey]
}

// TestReconcileAltinity_RecordsNoArchiveTheJobWasNotGiven covers a Job whose
// Pod never carried the driver's archive name: one created before the
// upgrade that introduced the names, or rendered from a strategy that names
// its own archives. Recording the name would send the restore after an
// archive nobody wrote; recording none leaves it the fallback for Backups
// without a name.
func TestReconcileAltinity_RecordsNoArchiveTheJobWasNotGiven(t *testing.T) {
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	strategy := newAltinityArchiveStrategy("clickhouse-strategy")
	startedAt := metav1.NewTime(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	completedK8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobNameForBackupJob(backupJob), Namespace: backupJob.Namespace, OwnerReferences: altinityControllerRef(backupJob)},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name:  "clickhouse-backup",
					Image: "altinity/clickhouse-backup:test",
					Args:  []string{`target="clickhouse-ch-test-$(date -u +%Y%m%dT%H%M%SZ)"`},
				}},
			}},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
		Parameters: map[string]string{"bucketName": "ch-bucket"},
	}
	r, _, _ := newAltinityTestEnv(t, app,
		clientfake.NewClientBuilder().WithObjects(backupJob, strategy, completedK8sJob))
	ctx := context.Background()

	if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileAltinity() error = %v", err)
	}
	created := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: backupJob.Name}, created); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	if got, ok := created.Spec.DriverMetadata[altinityArchiveMetaKey]; ok {
		t.Errorf("Backup records archive %q its Job was never given", got)
	}
	if got := created.Spec.DriverMetadata[altinityParamPrefix+"bucketName"]; got != "ch-bucket" {
		t.Errorf("expected driverMetadata[parameter/bucketName]=ch-bucket, got %q", got)
	}
}

// TestReconcileAltinity_BackupNameAlreadyTaken covers a completed Job whose
// Backup name is already in use. A Backup outlives its BackupJob, so a
// BackupJob re-created under an earlier run's name finds that run's Backup,
// and claiming it would point the new run at an archive it did not write.
// A Backup recording this run's own archive is the one an earlier reconcile
// created before its status write was lost, and is adopted.
func TestReconcileAltinity_BackupNameAlreadyTaken(t *testing.T) {
	const thisRun = "ch-test-20260305050607-testbjuid"
	cases := []struct {
		name          string
		existing      string // archive the existing Backup records; empty for none
		wantSucceeded bool
	}{
		{name: "earlier run's Backup records another archive", existing: "ch-test-20260304050607-earlierbjuid"},
		{name: "earlier run's Backup records no archive", existing: ""},
		{name: "this run's Backup from a reconcile whose status write was lost", existing: thisRun, wantSucceeded: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newAltinityClickHouseApp("ch-test", "tenant-test")
			strategy := newAltinityArchiveStrategy("clickhouse-strategy")
			startedAt := metav1.NewTime(time.Date(2026, 3, 5, 5, 6, 7, 0, time.UTC))
			backupJob := &backupsv1alpha1.BackupJob{
				ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test", UID: "test-bj-uid"},
				Spec: backupsv1alpha1.BackupJobSpec{
					ApplicationRef:  newAltinityClickHouseRef("ch-test"),
					BackupClassName: "clickhouse-backup",
				},
				Status: backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt, Phase: backupsv1alpha1.BackupJobPhaseRunning},
			}
			driverMD := map[string]string{altinityParamPrefix + "bucketName": "ch-bucket"}
			if tc.existing != "" {
				driverMD[altinityArchiveMetaKey] = tc.existing
			}
			existing := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: backupJob.Name, Namespace: backupJob.Namespace},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: newAltinityClickHouseRef("ch-test"),
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: new(strategyv1alpha1.GroupVersion.Group),
						Kind:     strategyv1alpha1.AltinityStrategyKind,
						Name:     "clickhouse-strategy",
					},
					TakenAt:        metav1.NewTime(time.Date(2026, 3, 4, 5, 6, 22, 0, time.UTC)),
					DriverMetadata: driverMD,
				},
			}
			resolved := &ResolvedBackupConfig{
				StrategyRef: corev1.TypedLocalObjectReference{
					APIGroup: new(strategyv1alpha1.GroupVersion.Group),
					Kind:     strategyv1alpha1.AltinityStrategyKind,
					Name:     "clickhouse-strategy",
				},
				Parameters: map[string]string{"bucketName": "ch-bucket"},
			}
			r, _, _ := newAltinityTestEnv(t, app,
				clientfake.NewClientBuilder().WithObjects(backupJob, strategy, existing))
			ctx := context.Background()

			if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
				t.Fatalf("reconcileAltinity() creating the Job: %v", err)
			}
			job := &batchv1.Job{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: jobNameForBackupJob(backupJob)}, job); err != nil {
				t.Fatalf("get backup Job: %v", err)
			}
			if got := job.Spec.Template.Spec.Containers[0].Args[1]; got != "--archive="+thisRun {
				t.Fatalf("backup Pod was rendered with %q, want --archive=%s", got, thisRun)
			}
			job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
			if err := r.Status().Update(ctx, job); err != nil {
				t.Fatalf("mark backup Job complete: %v", err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), backupJob); err != nil {
				t.Fatalf("refresh BackupJob: %v", err)
			}
			if _, err := r.reconcileAltinity(ctx, backupJob, resolved); err != nil {
				t.Fatalf("reconcileAltinity() on the completed Job: %v", err)
			}

			updated := &backupsv1alpha1.BackupJob{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
				t.Fatalf("get BackupJob: %v", err)
			}
			if tc.wantSucceeded {
				if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseSucceeded {
					t.Errorf("phase = %q (%s), want Succeeded", updated.Status.Phase, updated.Status.Message)
				}
				if updated.Status.BackupRef == nil || updated.Status.BackupRef.Name != existing.Name {
					t.Errorf("BackupRef = %v, want %s", updated.Status.BackupRef, existing.Name)
				}
			} else {
				if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
					t.Errorf("phase = %q, want Failed: the run would claim a Backup recording %q instead of %q", updated.Status.Phase, tc.existing, thisRun)
				}
				if updated.Status.BackupRef != nil {
					t.Errorf("BackupRef = %v, want none", updated.Status.BackupRef)
				}
				if !strings.Contains(updated.Status.Message, thisRun) {
					t.Errorf("failure message %q does not name the archive this run wrote", updated.Status.Message)
				}
			}
			kept := &backupsv1alpha1.Backup{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(existing), kept); err != nil {
				t.Fatalf("get existing Backup: %v", err)
			}
			if got := kept.Spec.DriverMetadata[altinityArchiveMetaKey]; got != tc.existing {
				t.Errorf("existing Backup now records archive %q, want %q left in place", got, tc.existing)
			}
		})
	}
}

func TestReconcileAltinityRestore_RendersTheBackupsArchive(t *testing.T) {
	cases := []struct {
		name        string
		archive     string // recorded on the Backup; empty for a Backup taken before names were recorded
		wantArchive string
	}{
		{name: "recorded archive", archive: "clickhouse-ch-test-20260304T050607Z", wantArchive: "--archive=clickhouse-ch-test-20260304T050607Z"},
		{name: "no recorded archive", archive: "", wantArchive: "--archive="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targetApp := newAltinityClickHouseApp("ch-test", "tenant-test")
			strategy := newAltinityArchiveStrategy("clickhouse-strategy")
			driverMD := map[string]string{altinityParamPrefix + "bucketName": "ch-bucket"}
			if tc.archive != "" {
				driverMD[altinityArchiveMetaKey] = tc.archive
			}
			now := metav1.Now()
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "ch-backup", Namespace: "tenant-test"},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: newAltinityClickHouseRef("ch-test"),
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: new(strategyv1alpha1.GroupVersion.Group),
						Kind:     strategyv1alpha1.AltinityStrategyKind,
						Name:     "clickhouse-strategy",
					},
					TakenAt:        metav1.NewTime(time.Date(2026, 3, 4, 8, 6, 22, 0, time.FixedZone("UTC+3", 3*3600))),
					DriverMetadata: driverMD,
				},
			}
			restoreJob := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-test"},
				Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backup.Name}},
				Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
			}
			_, rr, _ := newAltinityTestEnv(t, targetApp,
				clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
			ctx := context.Background()

			if _, err := rr.reconcileAltinityRestore(ctx, restoreJob, backup); err != nil {
				t.Fatalf("reconcileAltinityRestore: %v", err)
			}
			job := &batchv1.Job{}
			if err := rr.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: jobNameForRestoreJob(restoreJob)}, job); err != nil {
				t.Fatalf("get restore Job: %v", err)
			}
			args := job.Spec.Template.Spec.Containers[0].Args
			want := []string{"--mode=restore", tc.wantArchive, "--taken-at=20260304T050622Z", "--bucket=ch-bucket"}
			for i, w := range want {
				if i >= len(args) || args[i] != w {
					t.Errorf("rendered restore args[%d]: want %q, got %q", i, w, args)
				}
			}
		})
	}
}

func projectedCredentials(namespace string, updatedAt time.Time) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        "cozy-backups-creds",
			Annotations: map[string]string{credentialsUpdatedAtAnnotation: updatedAt.UTC().Format(time.RFC3339)},
		},
	}
}

// The ClickHouse sidecar reads the projected credentials from a mounted
// file that kubelet rewrites only some time after the Secret changes, so a
// strategy Job started right after a projection would reach a sidecar with
// no or stale credentials.
func TestReconcileAltinity_WaitsForProjectedCredentialsToReachTheSidecar(t *testing.T) {
	cases := []struct {
		name      string
		secret    *corev1.Secret
		jobExists bool
		wantWait  bool
	}{
		{name: "credentials changed just now", secret: projectedCredentials("tenant-test", time.Now()), wantWait: true},
		{name: "credentials changed long ago", secret: projectedCredentials("tenant-test", time.Now().Add(-altinityCredentialsPropagation-time.Second))},
		{name: "no projected credentials", secret: nil},
		{name: "the Job already runs", secret: projectedCredentials("tenant-test", time.Now()), jobExists: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := metav1.Now()
			backupJob := &backupsv1alpha1.BackupJob{
				ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
				Spec: backupsv1alpha1.BackupJobSpec{
					ApplicationRef:  newAltinityClickHouseRef("ch-test"),
					BackupClassName: "clickhouse-backup",
				},
				Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
			}
			objs := []client.Object{backupJob, newAltinityStrategy("clickhouse-strategy")}
			if tc.secret != nil {
				objs = append(objs, tc.secret)
			}
			if tc.jobExists {
				objs = append(objs, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-test", Name: jobNameForBackupJob(backupJob)}})
			}
			r, _, _ := newAltinityTestEnv(t, newAltinityClickHouseApp("ch-test", "tenant-test"), clientfake.NewClientBuilder().WithObjects(objs...))
			r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
			resolved := &ResolvedBackupConfig{
				StrategyRef: corev1.TypedLocalObjectReference{
					APIGroup: new(strategyv1alpha1.GroupVersion.Group),
					Kind:     strategyv1alpha1.AltinityStrategyKind,
					Name:     "clickhouse-strategy",
				},
			}
			ctx := context.Background()
			j := &backupsv1alpha1.BackupJob{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), j); err != nil {
				t.Fatalf("get BackupJob: %v", err)
			}
			res, err := r.reconcileAltinity(ctx, j, resolved)
			if err != nil {
				t.Fatalf("reconcileAltinity: %v", err)
			}
			jobs := &batchv1.JobList{}
			if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
				t.Fatalf("list Jobs: %v", err)
			}
			ready := meta.FindStatusCondition(j.Status.Conditions, "Ready")
			if tc.wantWait {
				if len(jobs.Items) != 0 {
					t.Errorf("a strategy Job was created %d time(s) before the credentials could reach the sidecar", len(jobs.Items))
				}
				if res.RequeueAfter <= 0 || res.RequeueAfter > altinityCredentialsPropagation {
					t.Errorf("RequeueAfter = %v, want within (0, %v]", res.RequeueAfter, altinityCredentialsPropagation)
				}
				if ready == nil || ready.Reason != "WaitingForCredentials" {
					t.Errorf("Ready = %+v, want WaitingForCredentials", ready)
				}
				return
			}
			if len(jobs.Items) != 1 {
				t.Errorf("got %d strategy Jobs, want 1", len(jobs.Items))
			}
			if ready != nil && ready.Reason == "WaitingForCredentials" {
				t.Errorf("Ready = %+v, the job should not wait", ready)
			}
		})
	}
}

func TestReconcileAltinityRestore_WaitsForProjectedCredentialsToReachTheSidecar(t *testing.T) {
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newAltinityClickHouseRef("ch-test"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: new(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.AltinityStrategyKind,
				Name:     "clickhouse-strategy",
			},
			TakenAt: now,
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backup.Name}},
		Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	_, rr, _ := newAltinityTestEnv(t, newAltinityClickHouseApp("ch-test", "tenant-test"),
		clientfake.NewClientBuilder().WithObjects(newAltinityStrategy("clickhouse-strategy"), backup, restoreJob,
			projectedCredentials("tenant-test", time.Now())))
	rr.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
	ctx := context.Background()
	rj := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), rj); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	res, err := rr.reconcileAltinityRestore(ctx, rj, backup)
	if err != nil {
		t.Fatalf("reconcileAltinityRestore: %v", err)
	}
	jobs := &batchv1.JobList{}
	if err := rr.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Errorf("a restore Job was created before the credentials could reach the sidecar")
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("RequeueAfter = %v, want a wait", res.RequeueAfter)
	}
	if ready := meta.FindStatusCondition(rj.Status.Conditions, "Ready"); ready == nil || ready.Reason != "WaitingForCredentials" {
		t.Errorf("Ready = %+v, want WaitingForCredentials", ready)
	}
}

// Without credentials projection configured there is no Secret to wait
// for; client-go refuses a Get with an empty name, so it must not be asked.
func TestReconcileAltinity_NoProjectionConfiguredDoesNotWait(t *testing.T) {
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
	}
	builder := clientfake.NewClientBuilder().
		WithObjects(backupJob, newAltinityStrategy("clickhouse-strategy")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Name == "" {
					return errors.New("resource name may not be empty")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		})
	r, _, _ := newAltinityTestEnv(t, newAltinityClickHouseApp("ch-test", "tenant-test"), builder)
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
	}
	ctx := context.Background()
	j := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), j); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if _, err := r.reconcileAltinity(ctx, j, resolved); err != nil {
		t.Fatalf("reconcileAltinity: %v", err)
	}
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("got %d strategy Jobs, want 1", len(jobs.Items))
	}
}

func projectedCredentialsStamped(namespace, stamp string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        "cozy-backups-creds",
			Annotations: map[string]string{credentialsUpdatedAtAnnotation: stamp},
		},
	}
}

func waitingForCredentials() []metav1.Condition {
	return []metav1.Condition{credentialsWaitCondition("cozy-backups-creds", time.Minute)}
}

// Once the hold is over and the Job runs, a Ready condition still saying the
// job is waiting for credentials describes a state the job has left.
func TestReconcileAltinity_ClearsTheCredentialsWaitOnceTheJobRuns(t *testing.T) {
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Conditions: waitingForCredentials()},
	}
	r, _, _ := newAltinityTestEnv(t, newAltinityClickHouseApp("ch-test", "tenant-test"),
		clientfake.NewClientBuilder().WithObjects(backupJob, newAltinityStrategy("clickhouse-strategy"),
			projectedCredentials("tenant-test", time.Now().Add(-altinityCredentialsPropagation-time.Second))).
			WithStatusSubresource(&backupsv1alpha1.BackupJob{}))
	r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
	}
	ctx := context.Background()
	j := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), j); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if _, err := r.reconcileAltinity(ctx, j, resolved); err != nil {
		t.Fatalf("reconcileAltinity: %v", err)
	}
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("got %d strategy Jobs, want 1", len(jobs.Items))
	}
	stored := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), stored); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if ready := meta.FindStatusCondition(stored.Status.Conditions, "Ready"); ready != nil && ready.Reason == "WaitingForCredentials" {
		t.Errorf("stored Ready = %+v while the Job runs", ready)
	}
}

func TestReconcileAltinityRestore_ClearsTheCredentialsWaitOnceTheJobRuns(t *testing.T) {
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newAltinityClickHouseRef("ch-test"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: new(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.AltinityStrategyKind,
				Name:     "clickhouse-strategy",
			},
			TakenAt: now,
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backup.Name}},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning,
			Conditions: waitingForCredentials()},
	}
	_, rr, _ := newAltinityTestEnv(t, newAltinityClickHouseApp("ch-test", "tenant-test"),
		clientfake.NewClientBuilder().WithObjects(newAltinityStrategy("clickhouse-strategy"), backup, restoreJob,
			projectedCredentials("tenant-test", time.Now().Add(-altinityCredentialsPropagation-time.Second))).
			WithStatusSubresource(&backupsv1alpha1.RestoreJob{}))
	rr.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
	ctx := context.Background()
	rj := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), rj); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	if _, err := rr.reconcileAltinityRestore(ctx, rj, backup); err != nil {
		t.Fatalf("reconcileAltinityRestore: %v", err)
	}
	stored := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), stored); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	if ready := meta.FindStatusCondition(stored.Status.Conditions, "Ready"); ready != nil && ready.Reason == "WaitingForCredentials" {
		t.Errorf("stored Ready = %+v while the Job runs", ready)
	}
}

// The stamp is only ever rewritten when the projected content changes, so a
// value the hold cannot use must not hold the job: an unreadable one would
// fail every reconcile, and one far in the future would wait again on each.
// A stamp a little ahead, as a skewed clock writes it, still waits, but only
// until that stamp plus the propagation time.
func TestCredentialsPropagationWait_UnusableStampsDoNotHoldTheJob(t *testing.T) {
	cases := []struct {
		name    string
		stamp   string
		wantMin time.Duration
		wantMax time.Duration
	}{
		{name: "unreadable", stamp: "garbage"},
		{name: "far in the future", stamp: "3000-01-01T00:00:00Z"},
		{name: "slightly ahead", stamp: time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339),
			wantMin: altinityCredentialsPropagation, wantMax: altinityCredentialsPropagation + 31*time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := clientfake.NewClientBuilder().WithObjects(projectedCredentialsStamped("tenant-test", tc.stamp)).Build()
			wait, err := credentialsPropagationWait(context.Background(), c, "cozy-backups-creds", "tenant-test", "job")
			if err != nil {
				t.Fatalf("credentialsPropagationWait: %v", err)
			}
			if wait < tc.wantMin || wait > tc.wantMax {
				t.Errorf("wait = %v, want within [%v, %v]", wait, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestClearCredentialsWait_KeepsAReadyConditionItDidNotSet(t *testing.T) {
	conditions := []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "StrategyNotReady"}}
	if clearCredentialsWait(&conditions) {
		t.Errorf("clearCredentialsWait reported clearing a Ready condition with reason StrategyNotReady")
	}
	if ready := meta.FindStatusCondition(conditions, "Ready"); ready == nil || ready.Reason != "StrategyNotReady" {
		t.Errorf("Ready = %+v, want the StrategyNotReady condition kept", ready)
	}
}

// A read the hold cannot complete must not release the Job: starting it
// before the credentials reach the sidecar is the failure the hold prevents.
func TestReconcileAltinity_UnreadableHoldStateDoesNotStartTheJob(t *testing.T) {
	cases := []struct {
		name string
		fail func(key client.ObjectKey, obj client.Object) bool
	}{
		{name: "credentials Secret", fail: func(key client.ObjectKey, obj client.Object) bool {
			_, isSecret := obj.(*corev1.Secret)
			return isSecret && key.Name == "cozy-backups-creds"
		}},
		{name: "strategy Job", fail: func(key client.ObjectKey, obj client.Object) bool {
			_, isJob := obj.(*batchv1.Job)
			return isJob
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := metav1.Now()
			backupJob := &backupsv1alpha1.BackupJob{
				ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
				Spec: backupsv1alpha1.BackupJobSpec{
					ApplicationRef:  newAltinityClickHouseRef("ch-test"),
					BackupClassName: "clickhouse-backup",
				},
				Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now},
			}
			builder := clientfake.NewClientBuilder().
				WithObjects(backupJob, newAltinityStrategy("clickhouse-strategy"),
					projectedCredentials("tenant-test", time.Now().Add(-altinityCredentialsPropagation-time.Second))).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if tc.fail(key, obj) {
							return apierrors.NewInternalError(errors.New("etcd unavailable"))
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				})
			r, _, _ := newAltinityTestEnv(t, newAltinityClickHouseApp("ch-test", "tenant-test"), builder)
			r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
			resolved := &ResolvedBackupConfig{
				StrategyRef: corev1.TypedLocalObjectReference{
					APIGroup: new(strategyv1alpha1.GroupVersion.Group),
					Kind:     strategyv1alpha1.AltinityStrategyKind,
					Name:     "clickhouse-strategy",
				},
			}
			ctx := context.Background()
			j := &backupsv1alpha1.BackupJob{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), j); err != nil {
				t.Fatalf("get BackupJob: %v", err)
			}
			if _, err := r.reconcileAltinity(ctx, j, resolved); err == nil {
				t.Errorf("reconcileAltinity returned no error although the %s could not be read", tc.name)
			}
			jobs := &batchv1.JobList{}
			if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
				t.Fatalf("list Jobs: %v", err)
			}
			if len(jobs.Items) != 0 {
				t.Errorf("a strategy Job was created although the %s could not be read", tc.name)
			}
		})
	}
}

func TestReconcileAltinityRestore_UnreadableHoldStateDoesNotStartTheJob(t *testing.T) {
	cases := []struct {
		name string
		fail func(key client.ObjectKey, obj client.Object) bool
	}{
		{name: "credentials Secret", fail: func(key client.ObjectKey, obj client.Object) bool {
			_, isSecret := obj.(*corev1.Secret)
			return isSecret && key.Name == "cozy-backups-creds"
		}},
		{name: "strategy Job", fail: func(key client.ObjectKey, obj client.Object) bool {
			_, isJob := obj.(*batchv1.Job)
			return isJob
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := metav1.Now()
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "ch-backup", Namespace: "tenant-test"},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: newAltinityClickHouseRef("ch-test"),
					StrategyRef: corev1.TypedLocalObjectReference{
						APIGroup: new(strategyv1alpha1.GroupVersion.Group),
						Kind:     strategyv1alpha1.AltinityStrategyKind,
						Name:     "clickhouse-strategy",
					},
					TakenAt: now,
				},
			}
			restoreJob := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
				Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backup.Name}},
				Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
			}
			builder := clientfake.NewClientBuilder().
				WithObjects(newAltinityStrategy("clickhouse-strategy"), backup, restoreJob,
					projectedCredentials("tenant-test", time.Now().Add(-altinityCredentialsPropagation-time.Second))).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if tc.fail(key, obj) {
							return apierrors.NewInternalError(errors.New("etcd unavailable"))
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				})
			_, rr, _ := newAltinityTestEnv(t, newAltinityClickHouseApp("ch-test", "tenant-test"), builder)
			rr.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
			ctx := context.Background()
			rj := &backupsv1alpha1.RestoreJob{}
			if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), rj); err != nil {
				t.Fatalf("get RestoreJob: %v", err)
			}
			if _, err := rr.reconcileAltinityRestore(ctx, rj, backup); err == nil {
				t.Errorf("reconcileAltinityRestore returned no error although the %s could not be read", tc.name)
			}
			jobs := &batchv1.JobList{}
			if err := rr.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
				t.Fatalf("list Jobs: %v", err)
			}
			if len(jobs.Items) != 0 {
				t.Errorf("a restore Job was created although the %s could not be read", tc.name)
			}
		})
	}
}

// A backup held for the credentials keeps the archive name it was given from
// its persisted StartedAt: the held reconciles write the job's status, and the
// Pod created after the hold, and the Backup recorded after it, must still
// name the same archive, which is stamped no later than the Backup's takenAt.
func TestReconcileAltinity_CredentialsWaitKeepsTheArchiveName(t *testing.T) {
	startedAt := metav1.NewTime(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test", UID: "0f3c9a1e-5b7d-4c2a-9e8f-1a2b3c4d5e6f"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	secret := projectedCredentials("tenant-test", time.Now())
	r, _, _ := newAltinityTestEnv(t, newAltinityClickHouseApp("ch-test", "tenant-test"),
		clientfake.NewClientBuilder().WithObjects(backupJob, newAltinityArchiveStrategy("clickhouse-strategy"), secret))
	r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     "clickhouse-strategy",
		},
	}
	ctx := context.Background()
	const want = "ch-test-20260304050607-0f3c9a1e5b7d"
	reconcile := func(step string) {
		t.Helper()
		j := &backupsv1alpha1.BackupJob{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), j); err != nil {
			t.Fatalf("%s: get BackupJob: %v", step, err)
		}
		if _, err := r.reconcileAltinity(ctx, j, resolved); err != nil {
			t.Fatalf("%s: reconcileAltinity: %v", step, err)
		}
	}

	reconcile("held")
	job := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: jobNameForBackupJob(backupJob)}, job); !apierrors.IsNotFound(err) {
		t.Fatalf("the Job exists while the credentials wait should hold it: %v", err)
	}

	if err := r.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
		t.Fatalf("get projected Secret: %v", err)
	}
	secret.Annotations[credentialsUpdatedAtAnnotation] = time.Now().Add(-altinityCredentialsPropagation - time.Second).UTC().Format(time.RFC3339)
	if err := r.Update(ctx, secret); err != nil {
		t.Fatalf("age the credentials stamp: %v", err)
	}
	reconcile("after the wait")
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: jobNameForBackupJob(backupJob)}, job); err != nil {
		t.Fatalf("get backup Job: %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Args[1]; got != "--archive="+want {
		t.Fatalf("backup Pod was rendered with %q after the wait, want --archive=%s", got, want)
	}

	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, job); err != nil {
		t.Fatalf("mark backup Job complete: %v", err)
	}
	reconcile("completed")
	created := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: backupJob.Name}, created); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	if got := created.Spec.DriverMetadata[altinityArchiveMetaKey]; got != want {
		t.Errorf("Backup records archive %q, want %q", got, want)
	}
	if stamp := created.Spec.TakenAt.UTC().Format(altinityArchiveTimeLayout); stamp < "20260304T050607Z" {
		t.Errorf("Backup takenAt %s is earlier than its archive's stamp", stamp)
	}
}

// TestReconcileAltinity_DoesNotReuseAJobItDoesNotControl covers a backup Job
// already standing under the BackupJob's Job name that this BackupJob did not
// create. Taking its completion for this run's would report a backup that
// never ran and record an archive nobody uploaded for it.
func TestReconcileAltinity_DoesNotReuseAJobItDoesNotControl(t *testing.T) {
	predecessor := runningAltinityBackupJob()
	predecessor.UID = "predecessor-uid"
	cases := []struct {
		name       string
		owners     []metav1.OwnerReference
		wantFailed bool
	}{
		{name: "an earlier BackupJob of the same name controls it", owners: altinityControllerRef(predecessor)},
		{name: "nothing controls it", wantFailed: true},
		{name: "a BackupJob of another name controls it", wantFailed: true, owners: altinityControllerRef(&backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Name: "other-bj", Namespace: "tenant-test", UID: "other-uid"},
		})},
		{name: "a RestoreJob controls it", wantFailed: true, owners: []metav1.OwnerReference{
			*metav1.NewControllerRef(predecessor, backupsv1alpha1.GroupVersion.WithKind("RestoreJob")),
		}},
		{name: "a BackupJob of another API group controls it", wantFailed: true, owners: []metav1.OwnerReference{
			*metav1.NewControllerRef(predecessor, schema.GroupVersion{Group: "example.com", Version: "v1alpha1"}.WithKind("BackupJob")),
		}},
	}
	for _, tc := range cases {
		// raced: the cache does not show the Job yet, so the driver meets it
		// only when its own create is refused.
		for _, raced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/raced=%t", tc.name, raced), func(t *testing.T) {
				app := newAltinityClickHouseApp("ch-test", "tenant-test")
				strategy := newAltinityArchiveStrategy("clickhouse-strategy")
				bj := runningAltinityBackupJob()
				bj.UID = "this-uid"
				job := completedAltinityK8sJob(bj)
				job.OwnerReferences = tc.owners
				builder := clientfake.NewClientBuilder().WithObjects(bj, strategy, job)
				if raced {
					hidden := false
					builder = builder.WithInterceptorFuncs(interceptor.Funcs{
						Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							if _, isJob := obj.(*batchv1.Job); isJob && !hidden {
								hidden = true
								return apierrors.NewNotFound(batchv1.Resource("jobs"), key.Name)
							}
							return c.Get(ctx, key, obj, opts...)
						},
					})
				}
				r, _, _ := newAltinityTestEnv(t, app, builder)
				ctx := context.Background()

				res, err := r.reconcileAltinity(ctx, bj, altinityResolved(strategy.Name))
				if err != nil {
					t.Fatalf("reconcileAltinity: %v", err)
				}

				got := &backupsv1alpha1.BackupJob{}
				if err := r.Get(ctx, client.ObjectKeyFromObject(bj), got); err != nil {
					t.Fatalf("get BackupJob: %v", err)
				}
				if got.Status.BackupRef != nil {
					t.Errorf("BackupJob points at Backup %q made from a Job it did not run", got.Status.BackupRef.Name)
				}
				if err := r.Get(ctx, client.ObjectKey{Namespace: bj.Namespace, Name: bj.Name}, &backupsv1alpha1.Backup{}); err == nil {
					t.Error("a Backup was created from a Job this BackupJob did not run")
				}
				if tc.wantFailed {
					if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
						t.Fatalf("phase = %q, want Failed", got.Status.Phase)
					}
					if !strings.Contains(got.Status.Message, job.Name) {
						t.Errorf("message %q does not name Job %s", got.Status.Message, job.Name)
					}
					return
				}
				if got.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
					t.Errorf("phase = %q, want Running while the earlier Job awaits garbage collection", got.Status.Phase)
				}
				if res.RequeueAfter <= 0 {
					t.Errorf("no requeue while the earlier Job awaits garbage collection: %+v", res)
				}
				if c := meta.FindStatusCondition(got.Status.Conditions, "Ready"); c == nil || !strings.Contains(c.Message, job.Name) {
					t.Errorf("Ready condition %+v does not say which Job it waits for", c)
				}
			})
		}
	}
}
