// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/foundationdbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/foundationdbtypes"
)

const fdbCleanupURL = "blobstore://@s3.example:443/tenant/bj-1?bucket=bkt&secure_connection=1"

func newFoundationDBCleanupClient(objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = foundationdbtypes.AddToScheme(s)
	_ = foundationdbapp.AddToScheme(s)
	return clientfake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&batchv1.Job{}).
		WithObjects(objs...).Build()
}

func newFoundationDBCleanupStrategy() *strategyv1alpha1.FoundationDB {
	tmpl := *newRenderedFoundationDBTemplate()
	tmpl.BackupDeploymentPodTemplateSpec = &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:         "foundationdb",
			Env:          []corev1.EnvVar{{Name: "FDB_BLOB_CREDENTIALS", Value: "/var/fdb-blob-credentials/blob_credentials.json"}},
			VolumeMounts: []corev1.VolumeMount{{Name: "blob-credentials", MountPath: "/var/fdb-blob-credentials"}},
		}},
		Volumes: []corev1.Volume{{Name: "blob-credentials", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: "cozy-backups-creds"},
		}}},
	}}
	return &strategyv1alpha1.FoundationDB{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-foundationdb"},
		Spec:       strategyv1alpha1.FoundationDBSpec{Template: tmpl},
	}
}

func newFoundationDBCleanupBackup(owned bool) *backupsv1alpha1.Backup {
	apps := foundationdbapp.GroupName
	apiGroup := strategyv1alpha1.GroupVersion.Group
	md := map[string]string{
		foundationdbBackupNameKey:      "bj-1-fdb",
		foundationdbBackupNamespaceKey: "tenant",
		foundationdbStorageURIKey:      fdbCleanupURL,
		foundationdbAgentVersionKey:    "7.3.79",
	}
	if owned {
		md[foundationdbOwnsDirectoryKey] = "true"
	}
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-1"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "FoundationDB", Name: "fdb-src", APIGroup: &apps},
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup, Kind: strategyv1alpha1.FoundationDBStrategyKind, Name: "cozy-default-foundationdb",
			},
			DriverMetadata: md,
		},
	}
}

func newFoundationDBOperatorBackup(state string, generation, reconciled int64) *foundationdbtypes.FoundationDBBackup {
	return &foundationdbtypes.FoundationDBBackup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-1-fdb", Generation: generation},
		Spec:       foundationdbtypes.FoundationDBBackupSpec{ClusterName: "foundationdb-fdb-src", Version: "7.3.79", BackupState: state},
		Status: foundationdbtypes.FoundationDBBackupStatus{
			Generations: &foundationdbtypes.BackupGenerationStatus{Reconciled: reconciled},
		},
	}
}

func listCleanupJobs(t *testing.T, c client.Client) []batchv1.Job {
	t.Helper()
	jobs := &batchv1.JobList{}
	if err := c.List(context.Background(), jobs, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	return jobs.Items
}

func TestCleanupFoundationDBBackup_PinnedDirectoryIsLeftAlone(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	c := newFoundationDBCleanupClient(ns, newFoundationDBCleanupStrategy(), newFoundationDBOperatorBackup(foundationdbtypes.BackupStateRunning, 1, 1))
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	res, err := r.cleanupFoundationDBBackup(context.Background(), newFoundationDBCleanupBackup(false))
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("expected an immediate release, got %+v, %v", res, err)
	}
	if jobs := listCleanupJobs(t, c); len(jobs) != 0 {
		t.Errorf("a Backup that does not own its directory must not start a delete Job, got %d", len(jobs))
	}
	got := &foundationdbtypes.FoundationDBBackup{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "tenant", Name: "bj-1-fdb"}, got); err != nil || got.Spec.BackupState != foundationdbtypes.BackupStateRunning {
		t.Errorf("operator CR must be untouched, got %+v, %v", got.Spec, err)
	}
}

func newFoundationDBCleanupCluster() *foundationdbtypes.FoundationDBCluster {
	return &foundationdbtypes.FoundationDBCluster{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "foundationdb-fdb-src"}}
}

func TestCleanupFoundationDBBackup_StopsThenDeletesOperatorBackup(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	c := newFoundationDBCleanupClient(ns, newFoundationDBCleanupStrategy(), newFoundationDBCleanupCluster(), newFoundationDBOperatorBackup(foundationdbtypes.BackupStateRunning, 1, 1))
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()
	key := client.ObjectKey{Namespace: "tenant", Name: "bj-1-fdb"}
	backup := newFoundationDBCleanupBackup(true)

	if res, err := r.cleanupFoundationDBBackup(ctx, backup); err != nil || res.RequeueAfter == 0 {
		t.Fatalf("stop pass: %+v, %v", res, err)
	}
	got := &foundationdbtypes.FoundationDBBackup{}
	if err := c.Get(ctx, key, got); err != nil || got.Spec.BackupState != foundationdbtypes.BackupStateStopped {
		t.Fatalf("expected the operator CR stopped, got %+v, %v", got.Spec, err)
	}
	if jobs := listCleanupJobs(t, c); len(jobs) != 0 {
		t.Fatalf("no delete Job may start while the agents may still write, got %d", len(jobs))
	}

	// The apiserver bumps the generation on the spec patch (the fake client
	// does not); until the operator reconciles it the CR stays.
	got.Generation = 2
	if err := c.Update(ctx, got); err != nil {
		t.Fatalf("bump generation: %v", err)
	}
	if _, err := r.cleanupFoundationDBBackup(ctx, backup); err != nil {
		t.Fatalf("wait pass: %v", err)
	}
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("CR deleted before the operator acted on the stop: %v", err)
	}

	got.Status.Generations = &foundationdbtypes.BackupGenerationStatus{Reconciled: got.Generation}
	if err := c.Update(ctx, got); err != nil {
		t.Fatalf("mark reconciled: %v", err)
	}
	if _, err := r.cleanupFoundationDBBackup(ctx, backup); err != nil {
		t.Fatalf("delete pass: %v", err)
	}
	if err := c.Get(ctx, key, got); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the operator CR deleted, got err=%v", err)
	}
}

func TestCleanupFoundationDBBackup_DeleteJobUsesStrategyPodTemplate(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	c := newFoundationDBCleanupClient(ns, newFoundationDBCleanupStrategy())
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	res, err := r.cleanupFoundationDBBackup(context.Background(), newFoundationDBCleanupBackup(true))
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while the Job runs, got %+v, %v", res, err)
	}
	jobs := listCleanupJobs(t, c)
	if len(jobs) != 1 {
		t.Fatalf("expected one delete Job, got %d", len(jobs))
	}
	pod := jobs[0].Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever || len(pod.Containers) != 1 {
		t.Fatalf("unexpected pod spec: %+v", pod)
	}
	ct := pod.Containers[0]
	if ct.Image != "foundationdb/foundationdb:7.3.79" {
		t.Errorf("image = %q, want the recorded agent version", ct.Image)
	}
	if len(ct.Command) != 3 || ct.Command[2] != foundationdbCleanupScript {
		t.Errorf("command = %v, want the fdbbackup delete script", ct.Command)
	}
	env := map[string]string{}
	for _, e := range ct.Env {
		env[e.Name] = e.Value
	}
	if env["FDB_BACKUP_URL"] != fdbCleanupURL || env["FDB_BLOB_CREDENTIALS"] == "" {
		t.Errorf("env must carry the backup URL and the strategy's credentials file, got %v", env)
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].Secret == nil || pod.Volumes[0].Secret.SecretName != "cozy-backups-creds" {
		t.Errorf("volumes must come from the strategy pod template, got %+v", pod.Volumes)
	}
}

func TestCleanupFoundationDBBackup_JobOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		condition   batchv1.JobConditionType
		wantRequeue bool
	}{
		{"complete releases the Backup", batchv1.JobComplete, false},
		{"failed retries with a fresh Job", batchv1.JobFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
			c := newFoundationDBCleanupClient(ns, newFoundationDBCleanupStrategy())
			r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
			ctx := context.Background()
			backup := newFoundationDBCleanupBackup(true)
			if _, err := r.cleanupFoundationDBBackup(ctx, backup); err != nil {
				t.Fatalf("first pass: %v", err)
			}
			job := listCleanupJobs(t, c)[0]
			job.Status.Conditions = []batchv1.JobCondition{{Type: tc.condition, Status: corev1.ConditionTrue}}
			if err := c.Status().Update(ctx, &job); err != nil {
				t.Fatalf("update job status: %v", err)
			}

			res, err := r.cleanupFoundationDBBackup(ctx, backup)
			if err != nil {
				t.Fatalf("second pass: %v", err)
			}
			if (res.RequeueAfter != 0) != tc.wantRequeue {
				t.Errorf("requeue = %v, want %v", res.RequeueAfter != 0, tc.wantRequeue)
			}
			if jobs := listCleanupJobs(t, c); len(jobs) != 0 {
				t.Errorf("the finished Job must be collected, got %d", len(jobs))
			}
		})
	}
}

func TestCleanupFoundationDBBackup_SkipAnnotationReleases(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	c := newFoundationDBCleanupClient(ns, newFoundationDBCleanupStrategy(), newFoundationDBOperatorBackup(foundationdbtypes.BackupStateRunning, 1, 1))
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	backup := newFoundationDBCleanupBackup(true)
	backup.Annotations = map[string]string{foundationdbSkipArtifactCleanupAnnotation: "true"}

	res, err := r.cleanupFoundationDBBackup(context.Background(), backup)
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("expected an immediate release, got %+v, %v", res, err)
	}
	if jobs := listCleanupJobs(t, c); len(jobs) != 0 {
		t.Errorf("skip annotation must not start a delete Job, got %d", len(jobs))
	}
}

func TestBackupReconcile_HoldsFinalizerWhileFoundationDBCleanupRuns(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	backup := newFoundationDBCleanupBackup(true)
	backup.Finalizers = []string{backupFinalizer}
	c := newFoundationDBCleanupClient(ns, backup, newFoundationDBCleanupStrategy())
	r := &BackupReconciler{Client: c, Scheme: c.Scheme(), Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()
	key := client.ObjectKeyFromObject(backup)

	if err := c.Delete(ctx, backup); err != nil {
		t.Fatalf("delete backup: %v", err)
	}
	if res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil || res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while the delete Job runs, got %+v, %v", res, err)
	}
	got := &backupsv1alpha1.Backup{}
	if err := c.Get(ctx, key, got); err != nil || !controllerutil.ContainsFinalizer(got, backupFinalizer) {
		t.Fatalf("the finalizer must stay while the directory is deleted: %v", err)
	}

	job := listCleanupJobs(t, c)[0]
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, &job); err != nil {
		t.Fatalf("update job status: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := c.Get(ctx, key, got); !apierrors.IsNotFound(err) {
		t.Errorf("expected the Backup removed after the directory was deleted, got err=%v", err)
	}
}

func TestCleanupFoundationDBBackup_ClusterGoneSkipsTheWait(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	// Stopped but never reconciled: the operator cannot act on it without
	// the cluster, which the application's deletion already removed.
	c := newFoundationDBCleanupClient(ns, newFoundationDBCleanupStrategy(), newFoundationDBOperatorBackup(foundationdbtypes.BackupStateStopped, 2, 1))
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()
	backup := newFoundationDBCleanupBackup(true)

	if _, err := r.cleanupFoundationDBBackup(ctx, backup); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant", Name: "bj-1-fdb"}, &foundationdbtypes.FoundationDBBackup{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the CR deleted without waiting for a cluster that is gone, got err=%v", err)
	}
	if _, err := r.cleanupFoundationDBBackup(ctx, backup); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if jobs := listCleanupJobs(t, c); len(jobs) != 1 {
		t.Errorf("expected the delete Job once the CR is gone, got %d", len(jobs))
	}
}

func TestCleanupFoundationDBBackup_WaitsForRunningRestore(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	restoring := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bj-1"}},
		Status:     backupsv1alpha1.RestoreJobStatus{Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	c := newFoundationDBCleanupClient(ns, newFoundationDBCleanupStrategy(), newFoundationDBCleanupCluster(), restoring,
		newFoundationDBOperatorBackup(foundationdbtypes.BackupStateRunning, 1, 1))
	r := &BackupReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()

	res, err := r.cleanupFoundationDBBackup(ctx, newFoundationDBCleanupBackup(true))
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while the restore runs, got %+v, %v", res, err)
	}
	got := &foundationdbtypes.FoundationDBBackup{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant", Name: "bj-1-fdb"}, got); err != nil || got.Spec.BackupState != foundationdbtypes.BackupStateRunning {
		t.Errorf("nothing may be stopped or deleted while a restore reads the backup: %+v, %v", got.Spec, err)
	}

	restoring.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
	if err := c.Update(ctx, restoring); err != nil {
		t.Fatalf("finish restore: %v", err)
	}
	if _, err := r.cleanupFoundationDBBackup(ctx, newFoundationDBCleanupBackup(true)); err != nil {
		t.Fatalf("after restore: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant", Name: "bj-1-fdb"}, got); err != nil || got.Spec.BackupState != foundationdbtypes.BackupStateStopped {
		t.Errorf("cleanup must proceed once the restore finished: %+v, %v", got.Spec, err)
	}
}

func TestCleanupFoundationDBBackup_FailedJobWarns(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	c := newFoundationDBCleanupClient(ns, newFoundationDBCleanupStrategy())
	rec := record.NewFakeRecorder(10)
	r := &BackupReconciler{Client: c, Recorder: rec}
	ctx := context.Background()
	backup := newFoundationDBCleanupBackup(true)
	if _, err := r.cleanupFoundationDBBackup(ctx, backup); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	job := listCleanupJobs(t, c)[0]
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, &job); err != nil {
		t.Fatalf("update job status: %v", err)
	}
	if _, err := r.cleanupFoundationDBBackup(ctx, backup); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	select {
	case e := <-rec.Events:
		if !strings.Contains(e, "Warning") || !strings.Contains(e, foundationdbSkipArtifactCleanupAnnotation) {
			t.Errorf("event %q must be a Warning that names the skip annotation", e)
		}
	default:
		t.Errorf("a failed delete must record a Warning event")
	}
}

func TestFoundationDBCleanupJobName(t *testing.T) {
	if got := foundationdbCleanupJobName("bj-1"); got != "bj-1-cleanup" {
		t.Errorf("short name: got %q", got)
	}
	long := strings.Repeat("a", 60)
	a, b := foundationdbCleanupJobName(long+"-x"), foundationdbCleanupJobName(long+"-y")
	if len(a) > 63 || len(b) > 63 {
		t.Errorf("job names exceed 63 characters: %d, %d", len(a), len(b))
	}
	if a == b {
		t.Errorf("long names that differ only past the cut must not collide: %q", a)
	}
}

// The script runs with a stub fdbbackup, so its exit-code handling is
// exercised as bash executes it.
func TestFoundationDBCleanupScript_ExitCodes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, tc := range []struct {
		name   string
		output string
		code   string
		want   int
	}{
		{"deleted", "The entire container has been deleted.", "0", 0},
		{"already gone counts as deleted", "ERROR: Backup does not exist", "1", 0},
		{"other failure fails the Job", "ERROR: HTTP response code not received or indicated failure", "1", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/bin/sh\necho '" + tc.output + "'\nexit " + tc.code + "\n"
			if err := os.WriteFile(filepath.Join(dir, "fdbbackup"), []byte(stub), 0o755); err != nil {
				t.Fatalf("write stub: %v", err)
			}
			cmd := exec.Command(bash, "-c", foundationdbCleanupScript)
			cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "FDB_BACKUP_URL=" + fdbCleanupURL}
			err := cmd.Run()
			got := 0
			if exitErr, ok := err.(*exec.ExitError); ok {
				got = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("run script: %v", err)
			}
			if got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}
