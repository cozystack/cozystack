// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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
				altinityArchiveMetaKey:     "clickhouse-ch-test-" + name,
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
		withUseSystemBucket(newAltinityClickHouseApp("ch-test", "tenant-test"), true),
	}
}

// newAltinityCleanupClient registers the ClickHouse application kind the
// cleanup path reads alongside what the Rabbitmq cleanup client carries.
func newAltinityCleanupClient(objs ...client.Object) (client.Client, *runtime.Scheme) {
	return newAltinityCleanupClientWith(interceptor.Funcs{}, objs...)
}

func newAltinityCleanupClientWith(funcs interceptor.Funcs, objs ...client.Object) (client.Client, *runtime.Scheme) {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	gv := schema.GroupVersion{Group: backupsv1alpha1.DefaultApplicationAPIGroup, Version: "v1alpha1"}
	s.AddKnownTypeWithName(gv.WithKind("ClickHouse"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("ClickHouseList"), &unstructured.UnstructuredList{})
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gv})
	mapper.Add(gv.WithKind("ClickHouse"), meta.RESTScopeNamespace)
	return clientfake.NewClientBuilder().WithScheme(s).WithRESTMapper(mapper).
		WithStatusSubresource(&batchv1.Job{}).
		WithInterceptorFuncs(funcs).
		WithObjects(objs...).Build(), s
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
	c, s := newAltinityCleanupClient(altinityCleanupFixtures(backup)...)
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
	if ctr.Image != "altinity/clickhouse-backup:test" {
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

// ownCleanupJob is the cleanup Job this Backup's own cleanup would create.
func ownCleanupJob(b *backupsv1alpha1.Backup) *batchv1.Job {
	return buildAltinityCleanupJob(b.Namespace, b.Name+"-cleanup", corev1.Container{Image: "img"}, nil, "host", "secret",
		b.Spec.DriverMetadata[altinityArchiveMetaKey])
}

func TestCleanupAltinityBackup_FailedJobIsRetried(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	failed := ownCleanupJob(backup)
	failed.Status.Conditions = []batchv1.JobCondition{{
		Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
		Reason: "DeadlineExceeded", Message: "Job was active longer than specified deadline",
	}}
	c, s := newAltinityCleanupClient(append(altinityCleanupFixtures(backup), failed)...)
	rec := record.NewFakeRecorder(10)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: rec}
	res, err := r.cleanupOnDelete(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("a failed delete must keep the Backup and requeue")
	}
	if len(rec.Events) == 0 {
		t.Error("a failed delete must surface as a Warning Event on the Backup")
	} else if ev := <-rec.Events; !strings.Contains(ev, "Warning") || !strings.Contains(ev, "ch-bk-cleanup") {
		t.Errorf("event %q must be a Warning naming the failed Job", ev)
	} else if !strings.Contains(ev, "DeadlineExceeded") || !strings.Contains(ev, "longer than specified deadline") {
		// The same reconcile deletes the Job and its Pods, so the Event is
		// the only place the failure is still readable.
		t.Errorf("event %q must carry the reason and message of the Job's Failed condition", ev)
	} else if strings.Contains(ev, "Pod log") {
		t.Errorf("event %q must not send the reader to a Pod log the reconcile deletes", ev)
	}
	if left := listJobs(t, c); len(left) != 0 {
		t.Errorf("expected the failed Job collected for a fresh attempt, %d left", len(left))
	}
}

// A cleanup Job left by an earlier Backup of the same name, released while
// its Job ran, deleted a different archive: its completion is not this one's.
func TestCleanupAltinityBackup_StaleJobForAnotherArchiveIsReplaced(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	stale := buildAltinityCleanupJob("tenant-test", "ch-bk-cleanup", corev1.Container{Image: "img"}, nil, "host", "secret", "clickhouse-ch-test-earlier")
	stale.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	c, s := newAltinityCleanupClient(append(altinityCleanupFixtures(backup), stale)...)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	res, err := r.cleanupOnDelete(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("a Job for another archive must not release the Backup")
	}
	if left := listJobs(t, c); len(left) != 0 {
		t.Errorf("expected the stale Job collected so a fresh one runs, %d left", len(left))
	}
}

func TestCleanupAltinityBackup_RunningJobRequeues(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	c, s := newAltinityCleanupClient(append(altinityCleanupFixtures(backup), ownCleanupJob(backup))...)
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
		mutate     func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object
		wantEvent  bool
		wantReason string
	}{
		"no archive recorded": {mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			delete(b.Spec.DriverMetadata, altinityArchiveMetaKey)
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
			b.Spec.DriverMetadata[altinityArchiveMetaKey] = "../other/x"
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
		"application gone": {wantEvent: true, wantReason: "application is gone", mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			return dropObject(objs, &unstructured.Unstructured{})
		}},
		"application moved off the system bucket": {wantEvent: true, wantReason: "no longer uses the system bucket", mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
			for _, o := range objs {
				if u, ok := o.(*unstructured.Unstructured); ok {
					withUseSystemBucket(u, false)
				}
			}
			return objs
		}},
		"API Secret gone": {wantEvent: true, mutate: func(b *backupsv1alpha1.Backup, objs []client.Object) []client.Object {
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
			c, s := newAltinityCleanupClient(objs...)
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
				} else if !strings.Contains(ev, tc.wantReason) {
					t.Errorf("event %q must give the reason %q", ev, tc.wantReason)
				}
			}
		})
	}
}

// With the ClickHouse kind no longer served there is no application to call.
func TestCleanupAltinityBackup_ApplicationKindNotServedReleases(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	c, s := newRabbitmqBackupReconcilerClient(altinityCleanupFixtures(backup)[:4]...)
	rec := record.NewFakeRecorder(10)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: rec}
	res, err := r.cleanupOnDelete(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if res.RequeueAfter != 0 || len(listJobs(t, c)) != 0 || len(rec.Events) == 0 {
		t.Errorf("expected release with a Warning Event and no Job, got %+v, %d events", res, len(rec.Events))
	}
}

// The skip annotation also reaps a delete Job already in flight.
func TestCleanupAltinityBackup_SkipAnnotationReapsInflightJob(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	backup.Annotations = map[string]string{"backups.cozystack.io/skip-artifact-cleanup": "true"}
	c, s := newAltinityCleanupClient(append(altinityCleanupFixtures(backup), ownCleanupJob(backup))...)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.cleanupOnDelete(context.Background(), backup); err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if left := listJobs(t, c); len(left) != 0 {
		t.Errorf("expected the in-flight Job reaped, %d left", len(left))
	}
}

// A Job of the same name this controller did not create (the ClickHouse
// chart's own cleanup hook is <release>-cleanup) is never deleted or read as
// the outcome of the archive delete.
func TestCleanupAltinityBackup_LeavesForeignJobOfTheSameName(t *testing.T) {
	for name, skip := range map[string]bool{"cleanup": false, "skip annotation": true} {
		t.Run(name, func(t *testing.T) {
			backup := altinityCleanupBackup("ch-bk")
			if skip {
				backup.Annotations = map[string]string{"backups.cozystack.io/skip-artifact-cleanup": "true"}
			}
			foreign := ownCleanupJob(backup)
			foreign.Labels = map[string]string{"app.kubernetes.io/name": "clickhouse"}
			foreign.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
			c, s := newAltinityCleanupClient(append(altinityCleanupFixtures(backup), foreign)...)
			rec := record.NewFakeRecorder(10)
			r := &BackupReconciler{Client: c, Scheme: s, Recorder: rec}
			res, err := r.cleanupOnDelete(context.Background(), backup)
			if err != nil {
				t.Fatalf("cleanupOnDelete: %v", err)
			}
			if left := listJobs(t, c); len(left) != 1 {
				t.Fatalf("the foreign Job must be left alone, %d jobs", len(left))
			}
			if res.RequeueAfter != 0 || res.Requeue {
				t.Errorf("expected the Backup released, got %+v", res)
			}
			if !skip && len(rec.Events) == 0 {
				t.Error("a release because of a foreign Job must surface as a Warning Event")
			}
		})
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

// runAltinityCleanupScript runs the cleanup Pod's script with a curl stub that
// answers with the given body and HTTP status, and returns the script's exit
// status and the URL and method the stub was called with.
func runAltinityCleanupScript(t *testing.T, body, code string) (int, string) {
	t.Helper()
	exit, calls := runAltinityCleanupScriptSeq(t, body, code)
	return exit, calls[len(calls)-1]
}

// runAltinityCleanupScriptSeq answers the n-th curl call with the n-th of the
// space-separated codes, repeating the last one. sleep is a no-op and every
// date call advances the clock by 100 seconds, so a wait loop ends at once.
func runAltinityCleanupScriptSeq(t *testing.T, body, codes string) (int, []string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	stubs := map[string]string{
		"curl":  "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$STUB_LOG\"\nn=$(wc -l < \"$STUB_LOG\")\nset -- $STUB_CODES\ncode=$1\ni=1\nfor c in \"$@\"; do [ \"$i\" -le \"$n\" ] && code=$c; i=$((i+1)); done\nprintf '%s\\n%s' \"$STUB_BODY\" \"$code\"\n",
		"sleep": "#!/bin/sh\nexit 0\n",
		"date":  "#!/bin/sh\necho x >> \"$STUB_CLOCK\"\necho $((1000 + 100 * $(wc -l < \"$STUB_CLOCK\")))\n",
	}
	for name, src := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	logPath := filepath.Join(dir, "log")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", altinityCleanupScript)
	cmd.Env = []string{
		"PATH=" + dir + ":" + os.Getenv("PATH"),
		"STUB_LOG=" + logPath, "STUB_BODY=" + body, "STUB_CODES=" + codes,
		"STUB_CLOCK=" + filepath.Join(dir, "clock"),
		"API_HOST=chi-x.ns.svc", "ARCHIVE_NAME=clickhouse-x-bk",
		"API_USERNAME=u", "API_PASSWORD=p",
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("script still running after %v: %v", 30*time.Second, ctx.Err())
	}
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
	return exit, strings.Split(strings.TrimSpace(string(called)), "\n")
}

// The sidecar answers 423 to a delete while any other operation runs, such as
// a backup of the same application; failing the Job on it would spin a new
// Job and a Warning Event every few seconds for the whole backup.
func TestAltinityCleanupScript_WaitsForABusySidecar(t *testing.T) {
	exit, calls := runAltinityCleanupScriptSeq(t, `{"status":"success"}`, "423 423 200")
	if exit != 0 {
		t.Errorf("exit = %d, a delete that went through after the sidecar was busy must succeed", exit)
	}
	if len(calls) != 3 {
		t.Errorf("curl called %d times, want the delete retried until it was accepted", len(calls))
	}
}

// The wait is bounded: a sidecar that stays busy fails the Job before its
// activeDeadlineSeconds, and the controller starts a fresh one.
func TestAltinityCleanupScript_GivesUpOnASidecarThatStaysBusy(t *testing.T) {
	exit, calls := runAltinityCleanupScriptSeq(t, `{"status":"error","error":"another operation is currently running"}`, "423")
	if exit == 0 {
		t.Error("exit 0 although the delete never ran")
	}
	if len(calls) > 10 {
		t.Errorf("curl called %d times, want the wait bounded by the time budget", len(calls))
	}
}

func TestAltinityCleanupScript_CallsTheRemoteDeleteRoute(t *testing.T) {
	exit, called := runAltinityCleanupScript(t, `{"status":"success"}`, "200")
	if exit != 0 {
		t.Errorf("exit = %d on 200", exit)
	}
	if !strings.Contains(called, "-X POST") || !strings.Contains(called, "http://chi-x.ns.svc:7171/backup/delete/remote/clickhouse-x-bk") {
		t.Errorf("curl called with %q, want POST /backup/delete/remote/<archive>", called)
	}
	// Without a client-side limit a sidecar that never answers holds the Pod
	// until the Job's deadline kills it mid-request.
	if !strings.Contains(called, "--max-time ") {
		t.Errorf("curl called with %q, want the request bounded by the remaining budget", called)
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

// An application moved to its own bucket while the delete ran restarts its
// sidecar on that bucket, which answers "not found" for an archive left in
// the system bucket, and the Job completes. Releasing the Backup on that
// completion must say the archive may still be there.
func TestCleanupAltinityBackup_CompletionAfterLeavingTheSystemBucketWarns(t *testing.T) {
	for name, tc := range map[string]struct {
		onSystemBucket bool
		appGone        bool
		wantEvent      bool
	}{
		"still on the system bucket": {onSystemBucket: true},
		"moved to its own bucket":    {onSystemBucket: false, wantEvent: true},
		// The sidecar that answered was the application's own on the system
		// bucket; removing the application afterwards does not undo that.
		"removed after the delete": {appGone: true},
	} {
		t.Run(name, func(t *testing.T) {
			backup := altinityCleanupBackup("ch-bk")
			done := ownCleanupJob(backup)
			done.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
			objs := append(altinityCleanupFixtures(backup), done)
			for _, o := range objs {
				if u, ok := o.(*unstructured.Unstructured); ok {
					withUseSystemBucket(u, tc.onSystemBucket)
				}
			}
			if tc.appGone {
				objs = dropObject(objs, &unstructured.Unstructured{})
			}
			c, s := newAltinityCleanupClient(objs...)
			rec := record.NewFakeRecorder(10)
			r := &BackupReconciler{Client: c, Scheme: s, Recorder: rec}
			res, err := r.cleanupOnDelete(context.Background(), backup)
			if err != nil {
				t.Fatalf("cleanupOnDelete: %v", err)
			}
			if res.RequeueAfter != 0 || res.Requeue {
				t.Errorf("expected the Backup released on completion, got %+v", res)
			}
			if left := listJobs(t, c); len(left) != 0 {
				t.Errorf("expected the completed Job reaped, %d left", len(left))
			}
			if got := len(rec.Events) > 0; got != tc.wantEvent {
				t.Fatalf("warning event recorded = %v, want %v", got, tc.wantEvent)
			}
			if tc.wantEvent {
				if ev := <-rec.Events; !strings.Contains(ev, "ArtifactNotDeleted") || !strings.Contains(ev, "clickhouse-ch-test-ch-bk") || !strings.Contains(ev, "may remain") {
					t.Errorf("event %q must say the named archive may remain", ev)
				}
			}
		})
	}
}

// The cleanup Pod runs the strategy container's image, so it needs the pull
// settings the strategy's own Pod has; without them a private image never
// starts and the Backup stays terminating.
func TestCleanupAltinityBackup_CarriesTheStrategyPullSettings(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	objs := altinityCleanupFixtures(backup)
	for _, o := range objs {
		if s, ok := o.(*strategyv1alpha1.Altinity); ok {
			s.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry-creds"}}
			s.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullAlways
		}
	}
	c, s := newAltinityCleanupClient(objs...)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.cleanupOnDelete(context.Background(), backup); err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	jobs := listJobs(t, c)
	if len(jobs) != 1 {
		t.Fatalf("expected one cleanup Job, got %d", len(jobs))
	}
	pod := jobs[0].Spec.Template.Spec
	if !reflect.DeepEqual(pod.ImagePullSecrets, []corev1.LocalObjectReference{{Name: "registry-creds"}}) {
		t.Errorf("imagePullSecrets = %+v, want the strategy's", pod.ImagePullSecrets)
	}
	if got := pod.Containers[0].ImagePullPolicy; got != corev1.PullAlways {
		t.Errorf("imagePullPolicy = %q, want the strategy container's", got)
	}
}

// A cleanup Job the API server will never accept, such as one whose name
// <backup>-cleanup is too long for a Job, would otherwise be retried for good
// with the Backup stuck terminating.
func TestCleanupAltinityBackup_ReleasesWhenTheJobIsRefused(t *testing.T) {
	for name, refuse := range map[string]error{
		"invalid":   apierrors.NewInvalid(batchv1.SchemeGroupVersion.WithKind("Job").GroupKind(), "ch-bk-cleanup", nil),
		"forbidden": apierrors.NewForbidden(batchv1.Resource("jobs"), "ch-bk-cleanup", errors.New("quota")),
	} {
		t.Run(name, func(t *testing.T) {
			backup := altinityCleanupBackup("ch-bk")
			c, s := newAltinityCleanupClientWith(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*batchv1.Job); ok {
						return refuse
					}
					return c.Create(ctx, obj, opts...)
				},
			}, altinityCleanupFixtures(backup)...)
			rec := record.NewFakeRecorder(10)
			r := &BackupReconciler{Client: c, Scheme: s, Recorder: rec}
			res, err := r.cleanupOnDelete(context.Background(), backup)
			if err != nil {
				t.Fatalf("cleanupOnDelete: %v", err)
			}
			if res.RequeueAfter != 0 || res.Requeue {
				t.Errorf("expected the Backup released, got %+v", res)
			}
			if len(rec.Events) == 0 {
				t.Fatal("a refused cleanup Job must surface as a Warning Event")
			}
			if ev := <-rec.Events; !strings.Contains(ev, "ArtifactNotDeleted") || !strings.Contains(ev, "clickhouse-ch-test-ch-bk") {
				t.Errorf("event %q must name the archive left behind", ev)
			}
		})
	}
}

// Any other Create error is transient and goes back to the work queue with the
// Backup held.
func TestCleanupAltinityBackup_RetriesATransientCreateError(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	c, s := newAltinityCleanupClientWith(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return apierrors.NewServiceUnavailable("etcd leader change")
		},
	}, altinityCleanupFixtures(backup)...)
	rec := record.NewFakeRecorder(10)
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: rec}
	if _, err := r.cleanupOnDelete(context.Background(), backup); err == nil {
		t.Error("expected the error returned so the Backup is retried")
	}
	if len(rec.Events) != 0 {
		t.Errorf("a transient error must not release the Backup, got event %q", <-rec.Events)
	}
}

// A cleanup Job already being deleted has no outcome to read yet.
func TestCleanupAltinityBackup_WaitsForADeletingJob(t *testing.T) {
	backup := altinityCleanupBackup("ch-bk")
	deleting := ownCleanupJob(backup)
	deleting.Finalizers = []string{"batch.kubernetes.io/job-tracking"}
	deleting.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	c, s := newAltinityCleanupClient(append(altinityCleanupFixtures(backup), deleting)...)
	if err := c.Delete(context.Background(), deleting); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	r := &BackupReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	res, err := r.cleanupOnDelete(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the Job is being deleted, not a release on its stale completion")
	}
}
