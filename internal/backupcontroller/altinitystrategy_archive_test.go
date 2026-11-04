// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// newAltinityArchiveStrategy returns a strategy whose template consumes
// .ArchiveName, the way the shipped cozy-default-altinity does.
func newAltinityArchiveStrategy(name string) *strategyv1alpha1.Altinity {
	s := newAltinityStrategy(name)
	s.Spec.Template.Spec.Containers[0].Image = "backup-http-client:test"
	s.Spec.Template.Spec.Containers[0].Args = []string{
		"--mode={{ .Mode }}",
		"--archive={{ .ArchiveName }}",
	}
	return s
}

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
	now := metav1.Now()
	return &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newAltinityClickHouseRef("ch-test"),
			BackupClassName: "clickhouse-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
}

func completedAltinityK8sJob(j *backupsv1alpha1.BackupJob) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobNameForBackupJob(j), Namespace: j.Namespace},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
		}},
	}
}

// completeAltinityBackup drives a BackupJob whose batch Job already completed
// through reconcileAltinity and returns the Backup it produced.
func completeAltinityBackup(t *testing.T, app *unstructured.Unstructured, strategy *strategyv1alpha1.Altinity) *backupsv1alpha1.Backup {
	t.Helper()
	bj := runningAltinityBackupJob()
	r, _, _ := newAltinityTestEnv(t, app,
		clientfake.NewClientBuilder().WithObjects(bj, strategy, completedAltinityK8sJob(bj)))
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

// The archive name is minted by the driver, not by the strategy Pod, so the
// Backup can name the archive it owns. The rendered backup Job must receive
// exactly the name the Backup later records.
func TestReconcileAltinity_RendersDriverMintedArchiveName(t *testing.T) {
	app := newAltinityClickHouseApp("ch-test", "tenant-test")
	strategy := newAltinityArchiveStrategy("clickhouse-strategy")
	bj := runningAltinityBackupJob()
	r, _, _ := newAltinityTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(bj, strategy))
	ctx := context.Background()
	if _, err := r.reconcileAltinity(ctx, bj, altinityResolved(strategy.Name)); err != nil {
		t.Fatalf("reconcileAltinity: %v", err)
	}
	job := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: bj.Namespace, Name: jobNameForBackupJob(bj)}, job); err != nil {
		t.Fatalf("get backup Job: %v", err)
	}
	args := job.Spec.Template.Spec.Containers[0].Args
	if want := "--archive=clickhouse-ch-test-test-bj"; len(args) < 2 || args[1] != want {
		t.Errorf("rendered args = %q, want %q at [1]", args, want)
	}
}

func TestReconcileAltinity_RecordsArchiveNameAndSystemBucketFlow(t *testing.T) {
	app := withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), true)
	got := completeAltinityBackup(t, app, newAltinityArchiveStrategy("clickhouse-strategy"))
	if v := got.Spec.DriverMetadata[altinityArchiveNameKey]; v != "clickhouse-ch-test-test-bj" {
		t.Errorf("driverMetadata[%s] = %q, want clickhouse-ch-test-test-bj", altinityArchiveNameKey, v)
	}
	if v := got.Spec.DriverMetadata[altinityUseSystemBucketKey]; v != "true" {
		t.Errorf("driverMetadata[%s] = %q, want true", altinityUseSystemBucketKey, v)
	}
}

func TestReconcileAltinity_RecordsLegacyBucketFlow(t *testing.T) {
	for name, app := range map[string]*unstructured.Unstructured{
		"explicit false": withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), false),
		"unset":          newAltinityClickHouseApp("ch-test", "tenant-test"),
	} {
		t.Run(name, func(t *testing.T) {
			got := completeAltinityBackup(t, app, newAltinityArchiveStrategy("clickhouse-strategy"))
			if v := got.Spec.DriverMetadata[altinityUseSystemBucketKey]; v != "false" {
				t.Errorf("driverMetadata[%s] = %q, want false", altinityUseSystemBucketKey, v)
			}
		})
	}
}

// A custom strategy that never reads .ArchiveName picks its own archive name,
// so recording the driver's name would point cleanup at an archive that does
// not exist.
func TestReconcileAltinity_DoesNotRecordArchiveNameTheTemplateIgnores(t *testing.T) {
	app := withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), true)
	got := completeAltinityBackup(t, app, newAltinityStrategy("clickhouse-strategy"))
	if v, ok := got.Spec.DriverMetadata[altinityArchiveNameKey]; ok {
		t.Errorf("driverMetadata[%s] = %q recorded for a template that does not use it", altinityArchiveNameKey, v)
	}
}

// The shipped template references .ArchiveName outside the backup branch of
// its script too, and text/template prints "<no value>" for a missing map key.
func TestRenderAltinityTemplate_ArchiveNameEmptyOnRestore(t *testing.T) {
	tmpl := newAltinityArchiveStrategy("s").Spec.Template
	backup := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{ApplicationRef: newAltinityClickHouseRef("ch")}}
	out, err := renderAltinityTemplate(tmpl, map[string]any{}, "ch", "ns", altinityModeRestore, nil, backup, "")
	if err != nil {
		t.Fatalf("renderAltinityTemplate: %v", err)
	}
	if got := out.Spec.Containers[0].Args[1]; got != "--archive=" {
		t.Errorf("restore render of .ArchiveName = %q, want empty", got)
	}
}
