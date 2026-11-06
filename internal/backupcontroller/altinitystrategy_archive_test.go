// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"fmt"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func withUseSystemBucket(app *unstructured.Unstructured, v bool) *unstructured.Unstructured {
	if err := unstructured.SetNestedField(app.Object, v, "spec", "backup", "useSystemBucket"); err != nil {
		panic(err)
	}
	return app
}

func altinityResolved(strategyName string) *ResolvedBackupConfig {
	return &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.AltinityStrategyKind,
			Name:     strategyName,
		},
	}
}

func runningAltinityBackupJob() *backupsv1alpha1.BackupJob {
	startedAt := metav1.NewTime(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	return &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &startedAt, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
}

// completedAltinityK8sJob is a finished backup Job whose Pod was given the
// BackupJob's archive name.
func completedAltinityK8sJob(j *backupsv1alpha1.BackupJob) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobNameForBackupJob(j), Namespace: j.Namespace},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c", Args: []string{"--archive=" + altinityArchiveName(j)}}},
		}}},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
		}},
	}
}

// completeAltinityBackup drives a BackupJob whose batch Job already completed
// through reconcileAltinity and returns the Backup it produced.
func completeAltinityBackup(t *testing.T, app *unstructured.Unstructured, job func(*backupsv1alpha1.BackupJob) *batchv1.Job) *backupsv1alpha1.Backup {
	t.Helper()
	bj := runningAltinityBackupJob()
	strategy := newAltinityArchiveStrategy("clickhouse-strategy")
	r, _, _ := newAltinityTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(bj, strategy, job(bj)))
	ctx := context.Background()
	if _, err := r.reconcileAltinity(ctx, bj, altinityResolved(strategy.Name)); err != nil {
		t.Fatalf("reconcileAltinity: %v", err)
	}
	got := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: bj.Namespace, Name: bj.Name}, got); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	return got
}

// Cleanup deletes only the archive of a Backup taken while the application
// was set to the system bucket, so the Backup records that setting.
func TestReconcileAltinity_RecordsTheBucketFlow(t *testing.T) {
	for name, tc := range map[string]struct {
		app  *unstructured.Unstructured
		want string
	}{
		"system bucket":  {withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), true), "true"},
		"explicit false": {withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), false), "false"},
		"unset":          {newAltinityClickHouseApp("ch-test", "tenant-test"), "false"},
	} {
		t.Run(name, func(t *testing.T) {
			got := completeAltinityBackup(t, tc.app, completedAltinityK8sJob)
			if v := got.Spec.DriverMetadata[altinityUseSystemBucketKey]; v != tc.want {
				t.Errorf("driverMetadata[%s] = %q, want %q", altinityUseSystemBucketKey, v, tc.want)
			}
		})
	}
}

// The flow recorded on the Backup is the application's setting when the
// backup Job was created, not the one read when the controller next sees the
// Job complete: the application may switch buckets in between.
func TestReconcileAltinity_RecordsTheBucketFlowTheJobStartedWith(t *testing.T) {
	for _, tc := range []struct{ started, now bool }{{true, false}, {false, true}} {
		app := withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), tc.now)
		got := completeAltinityBackup(t, app, func(bj *backupsv1alpha1.BackupJob) *batchv1.Job {
			job := completedAltinityK8sJob(bj)
			job.Annotations = map[string]string{altinityUseSystemBucketKey: fmt.Sprint(tc.started)}
			return job
		})
		if v := got.Spec.DriverMetadata[altinityUseSystemBucketKey]; v != fmt.Sprint(tc.started) {
			t.Errorf("started on %v, now %v: recorded %q, want the flow the Job started with", tc.started, tc.now, v)
		}
	}
}

func TestReconcileAltinity_BackupJobCarriesTheBucketFlow(t *testing.T) {
	for _, flow := range []bool{true, false} {
		app := withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), flow)
		bj := runningAltinityBackupJob()
		strategy := newAltinityArchiveStrategy("clickhouse-strategy")
		r, _, _ := newAltinityTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(bj, strategy))
		ctx := context.Background()
		if _, err := r.reconcileAltinity(ctx, bj, altinityResolved(strategy.Name)); err != nil {
			t.Fatalf("reconcileAltinity: %v", err)
		}
		job := &batchv1.Job{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: bj.Namespace, Name: jobNameForBackupJob(bj)}, job); err != nil {
			t.Fatalf("get backup Job: %v", err)
		}
		if v := job.Annotations[altinityUseSystemBucketKey]; v != fmt.Sprint(flow) {
			t.Errorf("backup Job annotation = %q, want %v", v, flow)
		}
	}
}

func TestReconcileAltinity_FailsOnMalformedUseSystemBucket(t *testing.T) {
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	if err := unstructured.SetNestedField(app.Object, "yes", "spec", "backup", "useSystemBucket"); err != nil {
		t.Fatal(err)
	}
	bj := runningAltinityBackupJob()
	strategy := newAltinityArchiveStrategy("clickhouse-strategy")
	r, _, _ := newAltinityTestEnv(t, app,
		clientfake.NewClientBuilder().WithObjects(bj, strategy, completedAltinityK8sJob(bj)))
	ctx := context.Background()
	if _, err := r.reconcileAltinity(ctx, bj, altinityResolved(strategy.Name)); err != nil {
		t.Fatalf("reconcileAltinity: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("phase = %q, want Failed when the bucket flow cannot be read", got.Status.Phase)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(bj), &backupsv1alpha1.Backup{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected no Backup without a recorded bucket flow, got %v", err)
	}
}
