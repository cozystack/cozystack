// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
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

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/rabbitmqtypes"
)

func newRabbitmqApp(name, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   backupsv1alpha1.DefaultApplicationAPIGroup,
		Version: "v1alpha1",
		Kind:    rabbitmqAppKind,
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

func newRabbitmqRef(name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{
		APIGroup: stringPtr(backupsv1alpha1.DefaultApplicationAPIGroup),
		Kind:     rabbitmqAppKind,
		Name:     name,
	}
}

// newRabbitmqCluster builds the operator-side RabbitmqCluster the driver reads
// for its readiness gate. AllReplicasReady drives whether the precondition
// passes.
func newRabbitmqCluster(appName, namespace string, ready bool) *rabbitmqtypes.RabbitmqCluster {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	return &rabbitmqtypes.RabbitmqCluster{
		ObjectMeta: metav1.ObjectMeta{Name: rabbitmqClusterName(appName), Namespace: namespace},
		Status: rabbitmqtypes.RabbitmqClusterStatus{
			Conditions: []metav1.Condition{{
				Type:               rabbitmqtypes.ConditionAllReplicasReady,
				Status:             status,
				Reason:             "Test",
				Message:            "test condition",
				LastTransitionTime: metav1.Now(),
			}},
		},
	}
}

func newRabbitmqStrategy(name string) *strategyv1alpha1.Rabbitmq {
	return &strategyv1alpha1.Rabbitmq{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: strategyv1alpha1.RabbitmqSpec{
			ArtifactURITemplate: "s3://bkt/{{ .Release.Namespace }}/{{ .Release.Name }}/{{ .BackupName }}/definitions.json",
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "rabbitmq-backup",
						Image: "curl:test",
						Args: []string{
							"--host=rabbitmq-{{ .Release.Name }}.{{ .Release.Namespace }}.svc",
							"--mode={{ .Mode }}",
							"--uri={{ .ArtifactURI }}",
						},
					}},
				},
			},
		},
	}
}

func newRabbitmqResolved(strategyName string, params map[string]string) *ResolvedBackupConfig {
	return &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.RabbitmqStrategyKind,
			Name:     strategyName,
		},
		Parameters: params,
	}
}

func newRabbitmqTestEnv(t *testing.T, app *unstructured.Unstructured, builder *clientfake.ClientBuilder) (*BackupJobReconciler, *RestoreJobReconciler) {
	t.Helper()

	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)
	_ = rabbitmqtypes.AddToScheme(testScheme)

	gvr := schema.GroupVersionResource{
		Group:    backupsv1alpha1.DefaultApplicationAPIGroup,
		Version:  "v1alpha1",
		Resource: "rabbitmqs",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		testScheme,
		map[schema.GroupVersionResource]string{gvr: rabbitmqAppKind + "List"},
		app,
	)
	mapping := &meta.RESTMapping{
		Resource:         gvr,
		GroupVersionKind: app.GroupVersionKind(),
		Scope:            meta.RESTScopeNamespace,
	}
	restMapper := &mockRESTMapper{mapping: mapping}

	c := builder.WithScheme(testScheme).
		// Backup deliberately omitted: its real CRD has no status subresource,
		// which is exactly why createRabbitmqBackupArtifact persists
		// status.artifact on Create. Registering it here would make the fake
		// drop status on Create and hide a regression the driver depends on.
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}).
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
		}
}

func TestValidateRabbitmqApplicationRef(t *testing.T) {
	group := backupsv1alpha1.DefaultApplicationAPIGroup
	other := "example.com"
	tests := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{name: "RabbitMQ default group", ref: corev1.TypedLocalObjectReference{APIGroup: &group, Kind: "RabbitMQ"}},
		{name: "RabbitMQ empty group", ref: corev1.TypedLocalObjectReference{Kind: "RabbitMQ"}},
		{name: "wrong kind", ref: corev1.TypedLocalObjectReference{Kind: "Postgres"}, wantErr: true},
		{name: "wrong group", ref: corev1.TypedLocalObjectReference{APIGroup: &other, Kind: "RabbitMQ"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRabbitmqApplicationRef(tc.ref)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateRabbitmqApplicationRef(%+v) err=%v, wantErr=%v", tc.ref, err, tc.wantErr)
			}
		})
	}
}

func TestRabbitmqClusterName(t *testing.T) {
	if got := rabbitmqClusterName("foo"); got != "rabbitmq-foo" {
		t.Errorf("rabbitmqClusterName(foo) = %q, want rabbitmq-foo", got)
	}
}

func TestRabbitmqNotReadyMessage(t *testing.T) {
	ready := &rabbitmqtypes.RabbitmqCluster{
		Status: rabbitmqtypes.RabbitmqClusterStatus{Conditions: []metav1.Condition{
			{Type: rabbitmqtypes.ConditionAllReplicasReady, Status: metav1.ConditionTrue},
		}},
	}
	if msg := rabbitmqNotReadyMessage(ready); msg != "" {
		t.Errorf("ready cluster: want empty message, got %q", msg)
	}

	notReady := &rabbitmqtypes.RabbitmqCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq-x"},
		Status: rabbitmqtypes.RabbitmqClusterStatus{Conditions: []metav1.Condition{
			{Type: rabbitmqtypes.ConditionAllReplicasReady, Status: metav1.ConditionFalse, Message: "starting"},
		}},
	}
	if msg := rabbitmqNotReadyMessage(notReady); !strings.Contains(msg, "AllReplicasReady=False") || !strings.Contains(msg, "starting") {
		t.Errorf("not-ready cluster: unexpected message %q", msg)
	}

	missing := &rabbitmqtypes.RabbitmqCluster{ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq-x"}}
	if msg := rabbitmqNotReadyMessage(missing); !strings.Contains(msg, "no AllReplicasReady") {
		t.Errorf("condition-less cluster: unexpected message %q", msg)
	}
}

func TestRenderRabbitmqArtifactURI(t *testing.T) {
	const tmpl = "s3://bkt/{{ .Release.Namespace }}/{{ .Release.Name }}/{{ .BackupName }}/definitions.json"

	ctx1 := rabbitmqRenderContext(nil, "app-test", "tenant-test", rabbitmqModeBackup, "bj-1", "", nil, nil)
	uri1, err := renderRabbitmqArtifactURI(tmpl, ctx1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "s3://bkt/tenant-test/app-test/bj-1/definitions.json"; uri1 != want {
		t.Errorf("uri = %q, want %q", uri1, want)
	}

	// A second backup of the SAME application must render a DISTINCT key, or the
	// backups would overwrite each other in object storage and restoring an
	// older Backup would read the latest data.
	ctx2 := rabbitmqRenderContext(nil, "app-test", "tenant-test", rabbitmqModeBackup, "bj-2", "", nil, nil)
	uri2, err := renderRabbitmqArtifactURI(tmpl, ctx2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uri2 == uri1 {
		t.Errorf("two backups of one application produced the same key %q — they would overwrite each other", uri1)
	}

	if _, err := renderRabbitmqArtifactURI("{{ .Missing", ctx1); err == nil {
		t.Error("expected error for malformed artifact URI template")
	}
}

func TestRabbitmqBackupParameters_RoundTrip(t *testing.T) {
	backup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{
			rabbitmqParamPrefix + "prefix": "p1",
			"other/key":                    "ignored",
			rabbitmqParamPrefix:            "ignored-empty-key",
		}},
	}
	got := rabbitmqBackupParameters(backup)
	if got["prefix"] != "p1" {
		t.Errorf("prefix = %q, want p1", got["prefix"])
	}
	if _, ok := got["other/key"]; ok {
		t.Errorf("non-parameter key leaked: %v", got)
	}
}

// TestReconcileRabbitmq_PreconditionGate pins that the readiness gate defers a
// run (Ready=False, no batch Job) until the RabbitmqCluster reports
// AllReplicasReady, then proceeds once it does.
func TestReconcileRabbitmq_PreconditionGate(t *testing.T) {
	newRunningBackupJob := func() *backupsv1alpha1.BackupJob {
		now := metav1.Now()
		return &backupsv1alpha1.BackupJob{
			ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
			Spec: backupsv1alpha1.BackupJobSpec{
				ApplicationRef:  newRabbitmqRef("rmq-test"),
				BackupClassName: "cozy-default",
			},
			Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
		}
	}

	t.Run("cluster not ready defers without creating a Job", func(t *testing.T) {
		app := newRabbitmqApp("rmq-test", "tenant-test")
		backupJob := newRunningBackupJob()
		cluster := newRabbitmqCluster("rmq-test", "tenant-test", false)
		r, _ := newRabbitmqTestEnv(t, app, clientfake.NewClientBuilder().
			WithObjects(backupJob, newRabbitmqStrategy("cozy-default-rabbitmq"), cluster))
		ctx := context.Background()

		res, err := r.reconcileRabbitmq(ctx, backupJob, newRabbitmqResolved("cozy-default-rabbitmq", nil))
		if err != nil {
			t.Fatalf("reconcileRabbitmq() error = %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Error("expected a requeue while the cluster is not ready")
		}
		jobs := &batchv1.JobList{}
		if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if len(jobs.Items) != 0 {
			t.Fatalf("expected no Job while the precondition is unmet, got %d", len(jobs.Items))
		}
		updated := &backupsv1alpha1.BackupJob{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
			t.Fatalf("get backupjob: %v", err)
		}
		cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "RabbitMQClusterNotReady" {
			t.Errorf("expected Ready=False/RabbitMQClusterNotReady, got %+v", cond)
		}
	})

	t.Run("cluster ready proceeds to create the Job", func(t *testing.T) {
		app := newRabbitmqApp("rmq-test", "tenant-test")
		backupJob := newRunningBackupJob()
		cluster := newRabbitmqCluster("rmq-test", "tenant-test", true)
		r, _ := newRabbitmqTestEnv(t, app, clientfake.NewClientBuilder().
			WithObjects(backupJob, newRabbitmqStrategy("cozy-default-rabbitmq"), cluster))
		ctx := context.Background()

		if _, err := r.reconcileRabbitmq(ctx, backupJob, newRabbitmqResolved("cozy-default-rabbitmq", nil)); err != nil {
			t.Fatalf("reconcileRabbitmq() error = %v", err)
		}
		jobs := &batchv1.JobList{}
		if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if len(jobs.Items) != 1 {
			t.Fatalf("expected the Job once the cluster is ready, got %d", len(jobs.Items))
		}
		args := jobs.Items[0].Spec.Template.Spec.Containers[0].Args
		want := []string{"--host=rabbitmq-rmq-test.tenant-test.svc", "--mode=backup"}
		for i, w := range want {
			if i >= len(args) || args[i] != w {
				t.Errorf("rendered args[%d]: want %q, got %q", i, w, args)
			}
		}
	})
}

// TestReconcileRabbitmq_RejectsWrongKind pins that a non-RabbitMQ applicationRef
// fails the BackupJob terminally rather than spawning a Job.
func TestReconcileRabbitmq_RejectsWrongKind(t *testing.T) {
	app := newRabbitmqApp("rmq-test", "tenant-test")
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  corev1.TypedLocalObjectReference{APIGroup: stringPtr(backupsv1alpha1.DefaultApplicationAPIGroup), Kind: "Postgres", Name: "rmq-test"},
			BackupClassName: "cozy-default",
		},
	}
	r, _ := newRabbitmqTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob))
	ctx := context.Background()

	if _, err := r.reconcileRabbitmq(ctx, backupJob, newRabbitmqResolved("cozy-default-rabbitmq", nil)); err != nil {
		t.Fatalf("reconcileRabbitmq() error = %v", err)
	}
	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed for wrong kind, got %q", updated.Status.Phase)
	}
}

// rabbitmqBackup builds a source Backup produced by the Rabbitmq strategy, for
// the restore-path tests.
func rabbitmqBackup(sourceApp string) *backupsv1alpha1.Backup {
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "rmq-src-adhoc", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newRabbitmqRef(sourceApp),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.RabbitmqStrategyKind,
				Name:     "cozy-default-rabbitmq",
			},
			TakenAt: metav1.Now(),
		},
		Status: backupsv1alpha1.BackupStatus{
			Artifact: &backupsv1alpha1.BackupArtifact{
				URI: "s3://bkt/tenant-test/" + sourceApp + "/rmq-src-adhoc/definitions.json",
			},
		},
	}
}

func rabbitmqRunningRestoreJob(name, targetApp string) *backupsv1alpha1.RestoreJob {
	now := metav1.Now()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-test"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "rmq-src-adhoc"},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	if targetApp != "" {
		rj.Spec.TargetApplicationRef = &corev1.TypedLocalObjectReference{
			APIGroup: stringPtr(backupsv1alpha1.DefaultApplicationAPIGroup),
			Kind:     rabbitmqAppKind,
			Name:     targetApp,
		}
	}
	return rj
}

// TestReconcileRabbitmqRestore_ToCopy pins the to-copy path: the restore Job
// renders against the TARGET (targetApplicationRef override), not the source.
func TestReconcileRabbitmqRestore_ToCopy(t *testing.T) {
	targetApp := newRabbitmqApp("rmq-copy", "tenant-test")
	restoreJob := rabbitmqRunningRestoreJob("rmq-src-to-copy", "rmq-copy")
	backup := rabbitmqBackup("rmq-src")
	cluster := newRabbitmqCluster("rmq-copy", "tenant-test", true)

	_, rr := newRabbitmqTestEnv(t, targetApp, clientfake.NewClientBuilder().
		WithObjects(newRabbitmqStrategy("cozy-default-rabbitmq"), backup, restoreJob, cluster))
	ctx := context.Background()

	if _, err := rr.reconcileRabbitmqRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileRabbitmqRestore() error = %v", err)
	}
	jobs := &batchv1.JobList{}
	if err := rr.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 restore Job, got %d", len(jobs.Items))
	}
	if got := jobs.Items[0].Labels[rabbitmqLabelMode]; got != rabbitmqModeRestore {
		t.Errorf("mode label = %q, want %q", got, rabbitmqModeRestore)
	}
	args := jobs.Items[0].Spec.Template.Spec.Containers[0].Args
	// --host renders to the TARGET broker (rmq-copy), proving targetApplicationRef
	// override drove the render; --mode=restore; --uri is the URI RECORDED on the
	// source Backup (not a reconstruction), proving restore reads status.artifact.uri.
	want := []string{
		"--host=rabbitmq-rmq-copy.tenant-test.svc",
		"--mode=restore",
		"--uri=s3://bkt/tenant-test/rmq-src/rmq-src-adhoc/definitions.json",
	}
	for i, w := range want {
		if i >= len(args) || args[i] != w {
			t.Errorf("rendered restore args[%d]: want %q, got %q", i, w, args)
		}
	}
}

// TestReconcileRabbitmqRestore_TargetNotFound pins that a to-copy target that is
// not deployed fails the RestoreJob terminally rather than hanging.
func TestReconcileRabbitmqRestore_TargetNotFound(t *testing.T) {
	// The dynamic client only serves rmq-src, so the rmq-missing target Get
	// returns NotFound.
	seeded := newRabbitmqApp("rmq-src", "tenant-test")
	restoreJob := rabbitmqRunningRestoreJob("rmq-src-to-missing", "rmq-missing")
	backup := rabbitmqBackup("rmq-src")

	_, rr := newRabbitmqTestEnv(t, seeded, clientfake.NewClientBuilder().
		WithObjects(newRabbitmqStrategy("cozy-default-rabbitmq"), backup, restoreJob))
	ctx := context.Background()

	if _, err := rr.reconcileRabbitmqRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileRabbitmqRestore() error = %v", err)
	}
	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("expected phase Failed for missing target, got %q", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.Message, "target RabbitMQ application not found") {
		t.Errorf("expected 'target not found' message, got %q", updated.Status.Message)
	}
}

// TestReconcileRabbitmqRestore_TargetClusterNotReady pins that the readiness
// gate defers a restore until the TARGET broker is ready.
func TestReconcileRabbitmqRestore_TargetClusterNotReady(t *testing.T) {
	targetApp := newRabbitmqApp("rmq-copy", "tenant-test")
	restoreJob := rabbitmqRunningRestoreJob("rmq-src-to-copy", "rmq-copy")
	backup := rabbitmqBackup("rmq-src")
	cluster := newRabbitmqCluster("rmq-copy", "tenant-test", false)

	_, rr := newRabbitmqTestEnv(t, targetApp, clientfake.NewClientBuilder().
		WithObjects(newRabbitmqStrategy("cozy-default-rabbitmq"), backup, restoreJob, cluster))
	ctx := context.Background()

	res, err := rr.reconcileRabbitmqRestore(ctx, restoreJob, backup)
	if err != nil {
		t.Fatalf("reconcileRabbitmqRestore() error = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the target cluster is not ready")
	}
	jobs := &batchv1.JobList{}
	if err := rr.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no restore Job while the target is not ready, got %d", len(jobs.Items))
	}
	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "RabbitMQClusterNotReady" {
		t.Errorf("expected Ready=False/RabbitMQClusterNotReady, got %+v", cond)
	}
}

// TestReconcileRabbitmqRestore_NoRecordedArtifactURI pins that a Backup with no
// recorded status.artifact.uri fails the restore terminally, instead of
// reconstructing a key that may not point at a real object.
func TestReconcileRabbitmqRestore_NoRecordedArtifactURI(t *testing.T) {
	targetApp := newRabbitmqApp("rmq-src", "tenant-test")
	restoreJob := rabbitmqRunningRestoreJob("rmq-src-in-place", "")
	backup := rabbitmqBackup("rmq-src")
	backup.Status.Artifact = nil

	_, rr := newRabbitmqTestEnv(t, targetApp, clientfake.NewClientBuilder().
		WithObjects(newRabbitmqStrategy("cozy-default-rabbitmq"), backup, restoreJob))
	ctx := context.Background()

	if _, err := rr.reconcileRabbitmqRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileRabbitmqRestore() error = %v", err)
	}
	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("expected phase Failed for a Backup with no artifact URI, got %q", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.Message, "no recorded artifact URI") {
		t.Errorf("expected 'no recorded artifact URI' message, got %q", updated.Status.Message)
	}
}

// TestReconcileRabbitmq_RequiresArtifactURITemplate pins that a strategy without
// artifactURITemplate fails the backup: the Backup would have no recorded
// location for a restore to read.
func TestReconcileRabbitmq_RequiresArtifactURITemplate(t *testing.T) {
	app := newRabbitmqApp("rmq-test", "tenant-test")
	strategy := newRabbitmqStrategy("cozy-default-rabbitmq")
	strategy.Spec.ArtifactURITemplate = ""
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newRabbitmqRef("rmq-test"),
			BackupClassName: "cozy-default",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	cluster := newRabbitmqCluster("rmq-test", "tenant-test", true)
	r, _ := newRabbitmqTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy, cluster))
	ctx := context.Background()

	if _, err := r.reconcileRabbitmq(ctx, backupJob, newRabbitmqResolved("cozy-default-rabbitmq", nil)); err != nil {
		t.Fatalf("reconcileRabbitmq() error = %v", err)
	}
	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed for a strategy with no artifactURITemplate, got %q", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.Message, "artifactURITemplate") {
		t.Errorf("expected 'artifactURITemplate' message, got %q", updated.Status.Message)
	}
}

// TestCreateRabbitmqBackupArtifact_ArtifactURI pins that a rendered artifact URI
// is recorded on the Backup, and an empty URI leaves status.artifact unset.
func TestCreateRabbitmqBackupArtifact_ArtifactURI(t *testing.T) {
	app := newRabbitmqApp("rmq-test", "tenant-test")
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: newRabbitmqRef("rmq-test"), BackupClassName: "cozy-default"},
	}
	r, _ := newRabbitmqTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob))
	ctx := context.Background()
	resolved := newRabbitmqResolved("cozy-default-rabbitmq", nil)

	const wantURI = "s3://bkt/tenant-test/rmq-test/definitions.json"
	if _, err := r.createRabbitmqBackupArtifact(ctx, backupJob, resolved, wantURI); err != nil {
		t.Fatalf("createRabbitmqBackupArtifact() with URI: %v", err)
	}
	// Re-Get, not the returned struct: the real Backup CRD has no status
	// subresource, so Create persists status.artifact - the re-Get is what
	// proves it, and would fail if a status subresource were ever added.
	created := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: "test-bj"}, created); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	if created.Status.Artifact == nil || created.Status.Artifact.URI != wantURI {
		t.Errorf("expected persisted artifact URI %q, got %+v", wantURI, created.Status.Artifact)
	}

	backupJob.Name = "test-bj-2"
	if _, err := r.createRabbitmqBackupArtifact(ctx, backupJob, resolved, ""); err != nil {
		t.Fatalf("createRabbitmqBackupArtifact() without URI: %v", err)
	}
	noURI := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: "test-bj-2"}, noURI); err != nil {
		t.Fatalf("get Backup (no URI): %v", err)
	}
	if noURI.Status.Artifact != nil {
		t.Errorf("expected no persisted artifact when URI is empty, got %+v", noURI.Status.Artifact)
	}
}

// TestCleanupRabbitmqBackup_SpawnsDeleteJob pins that deleting a Backup spawns
// an ownerless, self-cleaning Job that deletes the recorded object in cleanup
// mode - so a retention-pruned Plan does not leak objects.
func TestCleanupRabbitmqBackup_SpawnsDeleteJob(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)

	backup := rabbitmqBackup("rmq-src") // status.artifact.uri is set by the fixture
	strategy := newRabbitmqStrategy("cozy-default-rabbitmq")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}
	c := clientfake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&batchv1.Job{}).
		WithObjects(ns, backup, strategy).Build()
	// Empty CredentialsConfig: ProjectBackupCredentials is a no-op when disabled,
	// so the spawn path runs without a source Secret in the fake.
	r := &BackupReconciler{Client: c, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()

	// First pass: spawns the delete Job and requeues (waits) rather than
	// letting the Backup go.
	res, err := r.cleanupRabbitmqBackup(ctx, backup)
	if err != nil {
		t.Fatalf("cleanupRabbitmqBackup() error = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the delete Job runs (must not orphan the object)")
	}
	jobs := &batchv1.JobList{}
	if err := c.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 cleanup Job, got %d", len(jobs.Items))
	}
	job := jobs.Items[0]
	if job.Name != "rmq-src-adhoc-cleanup" {
		t.Errorf("job name = %q, want rmq-src-adhoc-cleanup", job.Name)
	}
	if len(job.OwnerReferences) != 0 {
		t.Errorf("cleanup Job must be ownerless so it survives Backup deletion, got %d ownerRefs", len(job.OwnerReferences))
	}
	if job.Labels[rabbitmqLabelMode] != rabbitmqModeCleanup {
		t.Errorf("mode label = %q, want %q", job.Labels[rabbitmqLabelMode], rabbitmqModeCleanup)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || job.Spec.ActiveDeadlineSeconds == nil {
		t.Error("expected TTLSecondsAfterFinished + ActiveDeadlineSeconds on the cleanup Job")
	}
	foundMode := false
	for _, a := range job.Spec.Template.Spec.Containers[0].Args {
		if a == "--mode=cleanup" {
			foundMode = true
		}
	}
	if !foundMode {
		t.Errorf("expected the pod rendered in cleanup mode (--mode=cleanup), got args %v", job.Spec.Template.Spec.Containers[0].Args)
	}

	// Once the delete Job completes, cleanup finishes (zero Result) and reaps
	// the Job - so the Backup is only released after the object is gone.
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, &job); err != nil {
		t.Fatalf("update job status: %v", err)
	}
	res, err = r.cleanupRabbitmqBackup(ctx, backup)
	if err != nil {
		t.Fatalf("cleanupRabbitmqBackup() second call error = %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Errorf("expected cleanup to finish once the Job completed, got requeue %+v", res)
	}
	if err := c.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Errorf("expected the completed cleanup Job to be reaped, got %d", len(jobs.Items))
	}
}

// TestCleanupRabbitmqBackup_NoArtifactNoop pins that a Backup with no recorded
// artifact spawns nothing (there is no object to delete).
func TestCleanupRabbitmqBackup_NoArtifactNoop(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)

	backup := rabbitmqBackup("rmq-src")
	backup.Status.Artifact = nil
	c := clientfake.NewClientBuilder().WithScheme(testScheme).WithObjects(backup, newRabbitmqStrategy("cozy-default-rabbitmq")).Build()
	r := &BackupReconciler{Client: c, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()

	res, err := r.cleanupRabbitmqBackup(ctx, backup)
	if err != nil {
		t.Fatalf("cleanupRabbitmqBackup() error = %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Errorf("expected no requeue when there is nothing to delete, got %+v", res)
	}
	jobs := &batchv1.JobList{}
	if err := c.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no cleanup Job when there is no artifact, got %d", len(jobs.Items))
	}
}

// TestReconcileRabbitmq_ReadinessDeadline pins that the AllReplicasReady wait is
// bounded: a broker that never becomes ready fails the BackupJob past the
// deadline instead of requeuing in phase=Running forever.
func TestReconcileRabbitmq_ReadinessDeadline(t *testing.T) {
	app := newRabbitmqApp("rmq-test", "tenant-test")
	strategy := newRabbitmqStrategy("cozy-default-rabbitmq")
	started := metav1.NewTime(time.Now().Add(-31 * time.Minute))
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: newRabbitmqRef("rmq-test"), BackupClassName: "cozy-default"},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &started, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	cluster := newRabbitmqCluster("rmq-test", "tenant-test", false) // never ready
	r, _ := newRabbitmqTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy, cluster))
	ctx := context.Background()

	if _, err := r.reconcileRabbitmq(ctx, backupJob, newRabbitmqResolved("cozy-default-rabbitmq", nil)); err != nil {
		t.Fatalf("reconcileRabbitmq() error = %v", err)
	}
	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed past the readiness deadline, got %q", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.Message, "did not become ready within") {
		t.Errorf("expected a deadline message, got %q", updated.Status.Message)
	}
}

// TestCleanupRabbitmqBackup_SkipAnnotation pins the escape hatch: an annotated
// Backup is released without spawning a delete Job, so an operator can remove a
// Backup stuck Terminating because object storage is unreachable.
func TestCleanupRabbitmqBackup_SkipAnnotation(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)

	backup := rabbitmqBackup("rmq-src")
	backup.Annotations = map[string]string{"backups.cozystack.io/skip-artifact-cleanup": "true"}
	c := clientfake.NewClientBuilder().WithScheme(testScheme).
		WithObjects(backup, newRabbitmqStrategy("cozy-default-rabbitmq")).Build()
	r := &BackupReconciler{Client: c, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()

	res, err := r.cleanupRabbitmqBackup(ctx, backup)
	if err != nil {
		t.Fatalf("cleanupRabbitmqBackup() error = %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Errorf("expected the annotation to release the Backup (no requeue), got %+v", res)
	}
	jobs := &batchv1.JobList{}
	if err := c.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no delete Job when cleanup is skipped by annotation, got %d", len(jobs.Items))
	}
}
