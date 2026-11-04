// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test", UID: "0f1e2d3c-aaaa-bbbb-cccc-000000000001"},
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
	if want := "--archive=clickhouse-ch-test-test-bj-0f1e2d3c"; len(args) < 2 || args[1] != want {
		t.Errorf("rendered args = %q, want %q at [1]", args, want)
	}
}

func TestReconcileAltinity_RecordsArchiveNameAndSystemBucketFlow(t *testing.T) {
	app := withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), true)
	got := completeAltinityBackup(t, app, newAltinityArchiveStrategy("clickhouse-strategy"))
	if v := got.Spec.DriverMetadata[altinityArchiveNameKey]; v != "clickhouse-ch-test-test-bj-0f1e2d3c" {
		t.Errorf("driverMetadata[%s] = %q, want clickhouse-ch-test-test-bj-0f1e2d3c", altinityArchiveNameKey, v)
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

// A BackupJob recreated under the name of a deleted one must not reuse its
// archive: clickhouse-backup resumes an upload into an existing name, and the
// old Backup's cleanup would delete the new archive.
func TestAltinityArchiveName_DiffersAcrossBackupJobsOfTheSameName(t *testing.T) {
	a := runningAltinityBackupJob()
	b := runningAltinityBackupJob()
	b.UID = "9a8b7c6d-aaaa-bbbb-cccc-000000000002"
	if na, nb := altinityArchiveName(a), altinityArchiveName(b); na == nb {
		t.Errorf("same archive name %q for two BackupJobs with different UIDs", na)
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

// The expression the shipped template uses, rendered through the driver.
func TestRenderAltinityTemplate_ShippedArchiveNameExpression(t *testing.T) {
	tmpl := newAltinityStrategy("s").Spec.Template
	tmpl.Spec.Containers[0].Args = []string{`target="{{ .ArchiveName | default "" }}"`}
	backup := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{ApplicationRef: newAltinityClickHouseRef("ch")}}
	for _, tc := range []struct{ mode, archive, want string }{
		{altinityModeBackup, "clickhouse-ch-bj-0f1e2d3c", `target="clickhouse-ch-bj-0f1e2d3c"`},
		{altinityModeRestore, "", `target=""`},
	} {
		out, err := renderAltinityTemplate(tmpl, map[string]any{}, "ch", "ns", tc.mode, nil, backup, tc.archive)
		if err != nil {
			t.Fatalf("render %s: %v", tc.mode, err)
		}
		if got := out.Spec.Containers[0].Args[0]; got != tc.want {
			t.Errorf("%s: rendered %q, want %q", tc.mode, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Cleanup on Backup deletion
// ---------------------------------------------------------------------------

func altinityCleanupBackup(name string) *backupsv1alpha1.Backup {
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newAltinityClickHouseRef("ch-test"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: new(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.AltinityStrategyKind,
				Name:     "cozy-default-altinity",
			},
			TakenAt: metav1.Now(),
			DriverMetadata: map[string]string{
				altinityArchiveNameKey:     "clickhouse-ch-test-" + name,
				altinityUseSystemBucketKey: "true",
			},
		},
	}
}

func altinityAPIAuthSecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "clickhouse-ch-test-backup-api-auth", Namespace: "tenant-test",
	}}
}

func altinityCleanupFixtures(backup *backupsv1alpha1.Backup) []client.Object {
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}},
		backup,
		newAltinityArchiveStrategy("cozy-default-altinity"),
		altinityAPIAuthSecret(),
	}
}

func listJobs(t *testing.T, c client.Client) []batchv1.Job {
	t.Helper()
	jobs := &batchv1.JobList{}
	if err := c.List(context.Background(), jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	return jobs.Items
}

func envValue(t *testing.T, c corev1.Container, name string) string {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value
		}
	}
	t.Fatalf("env %s not set on %s", name, c.Name)
	return ""
}

// Deleting a system-bucket Backup deletes its archive through the sidecar and
// holds the finalizer until that delete finished.
func TestBackupReconcile_HoldsFinalizerWhileAltinityCleanupRuns(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	backup.Finalizers = []string{backupFinalizer}
	c, s := newRabbitmqBackupReconcilerClient(altinityCleanupFixtures(backup)...)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()

	if err := c.Delete(ctx, backup); err != nil {
		t.Fatalf("delete backup: %v", err)
	}
	key := client.ObjectKeyFromObject(backup)
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the cleanup Job runs")
	}
	got := &backupsv1alpha1.Backup{}
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("Backup must still exist while cleanup runs: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, backupFinalizer) {
		t.Error("expected the finalizer retained while the cleanup Job runs")
	}

	jobs := listJobs(t, c)
	if len(jobs) != 1 {
		t.Fatalf("expected one cleanup Job, got %d", len(jobs))
	}
	job := jobs[0]
	if job.Name != "ch-bk-cleanup" {
		t.Errorf("cleanup Job name = %q", job.Name)
	}
	if len(job.OwnerReferences) != 0 {
		t.Errorf("cleanup Job must be ownerless so it outlives the Backup, got %+v", job.OwnerReferences)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || job.Spec.TTLSecondsAfterFinished == nil {
		t.Error("cleanup Job needs activeDeadlineSeconds and a TTL as backstops")
	}
	pod := job.Spec.Template.Spec
	if len(pod.Containers) != 1 {
		t.Fatalf("expected one container, got %d", len(pod.Containers))
	}
	ctr := pod.Containers[0]
	if ctr.Image != "backup-http-client:test" {
		t.Errorf("image = %q, want the strategy container's image", ctr.Image)
	}
	if got := envValue(t, ctr, "ARCHIVE_NAME"); got != "clickhouse-ch-test-ch-bk" {
		t.Errorf("ARCHIVE_NAME = %q", got)
	}
	if got := envValue(t, ctr, "API_HOST"); got != "chi-clickhouse-ch-test-clickhouse-0-0.tenant-test.svc" {
		t.Errorf("API_HOST = %q", got)
	}
	for _, e := range ctr.Env {
		if e.Name == "API_USERNAME" || e.Name == "API_PASSWORD" {
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil ||
				e.ValueFrom.SecretKeyRef.Name != "clickhouse-ch-test-backup-api-auth" {
				t.Errorf("%s must come from the backup-api-auth Secret, got %+v", e.Name, e.ValueFrom)
			}
		}
	}
	if strings.Contains(strings.Join(append(ctr.Command, ctr.Args...), " "), "restore") {
		t.Error("cleanup Pod must never call a restore route")
	}

	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, &job); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile after completion: %v", err)
	}
	if err := c.Get(ctx, key, got); !apierrors.IsNotFound(err) {
		t.Errorf("expected the Backup removed after the cleanup Job completed, got %v", err)
	}
	if left := listJobs(t, c); len(left) != 0 {
		t.Errorf("expected the completed cleanup Job reaped, %d left", len(left))
	}
}

func TestCleanupAltinityBackup_FailedJobIsRetried(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	failed := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-bk-cleanup", Namespace: "tenant-test"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
		}},
	}
	c, s := newRabbitmqBackupReconcilerClient(append(altinityCleanupFixtures(backup), failed)...)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	res, err := r.cleanupOnDelete(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("a failed delete must keep the Backup and requeue")
	}
	if left := listJobs(t, c); len(left) != 0 {
		t.Errorf("expected the failed Job collected for a fresh attempt, %d left", len(left))
	}
}

func TestCleanupAltinityBackup_RunningJobRequeues(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	running := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "ch-bk-cleanup", Namespace: "tenant-test"}}
	c, s := newRabbitmqBackupReconcilerClient(append(altinityCleanupFixtures(backup), running)...)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	res, err := r.cleanupOnDelete(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the Job runs")
	}
	if left := listJobs(t, c); len(left) != 1 {
		t.Errorf("a running Job must be left alone, %d jobs", len(left))
	}
}

// Every case here lets the Backup go without spawning a Job: the archive is
// either not the driver's to delete or cannot be deleted from here.
func TestCleanupAltinityBackup_ReleasesWithoutJob(t *testing.T) {
	cases := map[string]struct {
		mutate    func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object
		wantEvent bool
	}{
		"no archive recorded": {mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			delete(b.Spec.DriverMetadata, altinityArchiveNameKey)
			return objs
		}},
		"legacy bucket flow": {mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			b.Spec.DriverMetadata[altinityUseSystemBucketKey] = "false"
			return objs
		}},
		"flow not recorded": {mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			delete(b.Spec.DriverMetadata, altinityUseSystemBucketKey)
			return objs
		}},
		"skip annotation": {mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			b.Annotations = map[string]string{"backups.cozystack.io/skip-artifact-cleanup": "true"}
			return objs
		}},
		"archive name unsafe in a URL path": {wantEvent: true, mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			b.Spec.DriverMetadata[altinityArchiveNameKey] = "../other/x"
			return objs
		}},
		"strategy gone": {wantEvent: true, mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			return dropObject(objs, &strategyv1alpha1.Altinity{})
		}},
		"strategy has no container": {wantEvent: true, mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			for _, o := range objs {
				if s, ok := o.(*strategyv1alpha1.Altinity); ok {
					s.Spec.Template.Spec.Containers = nil
				}
			}
			return objs
		}},
		"strategy container has no image": {wantEvent: true, mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			for _, o := range objs {
				if s, ok := o.(*strategyv1alpha1.Altinity); ok {
					s.Spec.Template.Spec.Containers[0].Image = ""
				}
			}
			return objs
		}},
		"application gone with its API Secret": {wantEvent: true, mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			return dropObject(objs, &corev1.Secret{})
		}},
		"namespace terminating": {wantEvent: true, mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			return dropObject(objs, &corev1.Namespace{})
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			backup := altinityCleanupBackup("ch-bk")
			objs := tc.mutate(backup, altinityCleanupFixtures(backup))
			c, s := newRabbitmqBackupReconcilerClient(objs...)
			rec := record.NewFakeRecorder(10)
			r := &BackupReconciler{Client: c, Scheme: s, Recorder: rec}
			res, err := r.cleanupOnDelete(context.Background(), backup)
			if err != nil {
				t.Fatalf("cleanupOnDelete: %v", err)
			}
			if res.RequeueAfter != 0 || res.Requeue {
				t.Errorf("expected the Backup released, got %+v", res)
			}
			if jobs := listJobs(t, c); len(jobs) != 0 {
				t.Errorf("expected no cleanup Job, got %d", len(jobs))
			}
			gotEvent := len(rec.Events) > 0
			if gotEvent != tc.wantEvent {
				t.Errorf("warning event recorded = %v, want %v", gotEvent, tc.wantEvent)
			}
			if gotEvent {
				if ev := <-rec.Events; !strings.Contains(ev, "ArtifactNotDeleted") || !strings.Contains(ev, "clickhouse-ch-test-ch-bk") && !strings.Contains(ev, "../other/x") {
					t.Errorf("event %q must name the archive left behind", ev)
				}
			}
		})
	}
}

// The skip annotation also reaps a delete Job already in flight.
func TestCleanupAltinityBackup_SkipAnnotationReapsInflightJob(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	backup.Annotations = map[string]string{"backups.cozystack.io/skip-artifact-cleanup": "true"}
	running := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "ch-bk-cleanup", Namespace: "tenant-test"}}
	c, s := newRabbitmqBackupReconcilerClient(append(altinityCleanupFixtures(backup), running)...)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.cleanupOnDelete(context.Background(), backup); err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if left := listJobs(t, c); len(left) != 0 {
		t.Errorf("expected the in-flight Job reaped, %d left", len(left))
	}
}

func dropObject(objs []client.Object, like client.Object) []client.Object {
	out := objs[:0:0]
	for _, o := range objs {
		if reflect.TypeOf(o) != reflect.TypeOf(like) {
			out = append(out, o)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Cleanup script, executed against a stub curl
// ---------------------------------------------------------------------------

// runAltinityCleanupScript runs the cleanup Pod's script with a curl stub that
// answers with the given body and HTTP status, and returns the script's exit
// status and the URL and method the stub was called with.
func runAltinityCleanupScript(t *testing.T, body, code string) (int, string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	stub := "#!/bin/sh\necho \"$*\" > \"$STUB_LOG\"\nprintf '%s\\n%s' \"$STUB_BODY\" \"$STUB_CODE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	logPath := filepath.Join(dir, "log")
	cmd := exec.Command("sh", "-c", altinityCleanupScript)
	cmd.Env = []string{
		"PATH=" + dir + ":" + os.Getenv("PATH"),
		"STUB_LOG=" + logPath, "STUB_BODY=" + body, "STUB_CODE=" + code,
		"API_HOST=chi-x.ns.svc", "ARCHIVE_NAME=clickhouse-x-bk",
		"API_USERNAME=u", "API_PASSWORD=p",
	}
	err := cmd.Run()
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run script: %v", err)
	}
	called, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("curl stub was not called: %v", rerr)
	}
	return exit, string(called)
}

func TestAltinityCleanupScript_CallsTheRemoteDeleteRoute(t *testing.T) {
	exit, called := runAltinityCleanupScript(t, `{"status":"success"}`, "200")
	if exit != 0 {
		t.Errorf("exit = %d on 200", exit)
	}
	if !strings.Contains(called, "-X POST") || !strings.Contains(called, "http://chi-x.ns.svc:7171/backup/delete/remote/clickhouse-x-bk") {
		t.Errorf("curl called with %q, want POST /backup/delete/remote/<archive>", called)
	}
}

func TestAltinityCleanupScript_ArchiveAlreadyGoneIsSuccess(t *testing.T) {
	exit, _ := runAltinityCleanupScript(t,
		`{"status":"error","operation":"delete","error":"'clickhouse-x-bk' is not found on remote storage"}`, "500")
	if exit != 0 {
		t.Errorf("exit = %d, an archive already gone must count as deleted", exit)
	}
}

func TestAltinityCleanupScript_FailsOnOtherErrors(t *testing.T) {
	for _, tc := range []struct{ body, code string }{
		{`{"status":"error","error":"another operation is currently running"}`, "423"},
		{`{"status":"error","error":"can't connect to clickhouse"}`, "500"},
		{`not found`, "404"},
		{``, "401"},
	} {
		if exit, _ := runAltinityCleanupScript(t, tc.body, tc.code); exit == 0 {
			t.Errorf("exit 0 on %s %q, want failure so the delete is retried", tc.code, tc.body)
		}
	}
}
