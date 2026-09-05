// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"strings"
	"testing"

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
)

// newRedisApp returns an unstructured apps.cozystack.io/Redis application the
// driver's dynamic client can serve. Carries a Ready=True condition (so the
// precondition passes) and a spec so tests can assert templates read the live
// object via .Application.
func newRedisApp(name, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   backupsv1alpha1.DefaultApplicationAPIGroup,
		Version: "v1alpha1",
		Kind:    redisAppKind,
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	u.Object["spec"] = map[string]any{"replicas": int64(3)}
	u.Object["status"] = map[string]any{
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True"},
		},
	}
	return u
}

func newRedisAppRef(name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{
		APIGroup: stringPtr(backupsv1alpha1.DefaultApplicationAPIGroup),
		Kind:     redisAppKind,
		Name:     name,
	}
}

// newRedisTestEnv mirrors newJobStrategyTestEnv but for the apps.cozystack.io/Redis
// kind. The dynamic fake matches GVR exactly, so the mapping must agree.
func newRedisTestEnv(t *testing.T, app *unstructured.Unstructured, builder *clientfake.ClientBuilder) (*BackupJobReconciler, *RestoreJobReconciler) {
	t.Helper()

	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)

	gvr := schema.GroupVersionResource{
		Group:    backupsv1alpha1.DefaultApplicationAPIGroup,
		Version:  "v1alpha1",
		Resource: "redises",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		testScheme,
		map[schema.GroupVersionResource]string{gvr: redisAppKind + "List"},
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
		}
}

// newRedisStrategy returns a Redis strategy whose template exercises every key
// the driver exposes: .Release, .Mode, .Parameters, and .Application.
func newRedisStrategy(name string) *strategyv1alpha1.Redis {
	return &strategyv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: strategyv1alpha1.RedisSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "backup",
						Image: "redis-backup:test",
						Args: []string{
							"--app={{ .Release.Name }}",
							"--mode={{ .Mode }}",
							"--bucket={{ .Parameters.bucketName }}",
							"--replicas={{ .Application.spec.replicas }}",
						},
					}},
				},
			},
		},
	}
}

func newRedisResolved(strategyName string, params map[string]string) *ResolvedBackupConfig {
	return &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.RedisStrategyKind,
			Name:     strategyName,
		},
		Parameters: params,
	}
}

func TestValidateRedisApplicationRef(t *testing.T) {
	group := backupsv1alpha1.DefaultApplicationAPIGroup
	other := "other.example.io"
	cases := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{"redis empty group", corev1.TypedLocalObjectReference{Kind: "Redis"}, false},
		{"redis default group", corev1.TypedLocalObjectReference{APIGroup: &group, Kind: "Redis"}, false},
		{"wrong kind", corev1.TypedLocalObjectReference{Kind: "Postgres"}, true},
		{"wrong group", corev1.TypedLocalObjectReference{APIGroup: &other, Kind: "Redis"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRedisApplicationRef(tc.ref)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateRedisApplicationRef(%+v) err=%v, wantErr=%v", tc.ref, err, tc.wantErr)
			}
		})
	}
}

func TestRedisAppReady(t *testing.T) {
	ready := map[string]interface{}{"status": map[string]interface{}{
		"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
	}}
	notReady := map[string]interface{}{"status": map[string]interface{}{
		"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "False", "reason": "HelmUpgradeFailed", "message": "boom"}},
	}}
	noCond := map[string]interface{}{"status": map[string]interface{}{}}

	if proceed, _, _ := redisAppReady(ready); !proceed {
		t.Error("Ready=True should proceed")
	}
	if proceed, reason, message := redisAppReady(notReady); proceed || reason != "HelmUpgradeFailed" || message != "boom" {
		t.Errorf("Ready=False should block with reason/message, got proceed=%v reason=%q message=%q", proceed, reason, message)
	}
	if proceed, _, _ := redisAppReady(noCond); !proceed {
		t.Error("absent Ready condition should proceed (never wedge backups on a missing condition)")
	}
}

func TestRedisStrategyParameters_RoundTrip(t *testing.T) {
	backup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{
				redisParamPrefix + "bucketName": "b1",
				redisParamPrefix + "endpoint":   "s3-host",
				"job.batch/name":                "ignored",
				"":                              "ignored",
			},
		},
	}
	got := redisStrategyParameters(backup)
	if got["bucketName"] != "b1" || got["endpoint"] != "s3-host" {
		t.Errorf("redisStrategyParameters = %v", got)
	}
	if _, ok := got["job.batch/name"]; ok {
		t.Errorf("non-parameter key leaked through: %v", got)
	}
}

func TestReconcileRedis_CreatesBatchJob(t *testing.T) {
	app := newRedisApp("cache", "tenant-test")
	strategy := newRedisStrategy("redis-strategy")

	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newRedisAppRef("cache"),
			BackupClassName: "cozy-default",
		},
	}
	resolved := newRedisResolved("redis-strategy", map[string]string{"bucketName": "redis-bucket"})

	r, _ := newRedisTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	ctx := context.Background()

	if _, err := r.reconcileRedis(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileRedis() first call: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), backupJob); err != nil {
		t.Fatalf("refresh BackupJob: %v", err)
	}
	if _, err := r.reconcileRedis(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileRedis() second call: %v", err)
	}

	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 batch/v1.Job, got %d", len(jobs.Items))
	}
	k8sJob := jobs.Items[0]
	if got := k8sJob.Name; got != jobNameForBackupJob(backupJob) {
		t.Errorf("expected Job name %q, got %q", jobNameForBackupJob(backupJob), got)
	}
	if got := k8sJob.Labels[backupsv1alpha1.OwningJobNameLabel]; got != backupJob.Name {
		t.Errorf("expected owning label %q, got %q", backupJob.Name, got)
	}
	if got := k8sJob.Labels[redisLabelMode]; got != redisModeBackup {
		t.Errorf("expected mode label %q, got %q", redisModeBackup, got)
	}
	args := k8sJob.Spec.Template.Spec.Containers[0].Args
	want := []string{"--app=cache", "--mode=backup", "--bucket=redis-bucket", "--replicas=3"}
	for i, w := range want {
		if i >= len(args) || args[i] != w {
			t.Errorf("rendered args[%d]: want %q, got %q", i, w, args)
			break
		}
	}

	if k8sJob.OwnerReferences == nil || k8sJob.OwnerReferences[0].Name != backupJob.Name || k8sJob.OwnerReferences[0].Controller == nil || !*k8sJob.OwnerReferences[0].Controller {
		t.Errorf("expected controller ownerRef on the BackupJob, got %v", k8sJob.OwnerReferences)
	}
}

func TestReconcileRedis_CompletesAndCreatesBackup(t *testing.T) {
	app := newRedisApp("cache", "tenant-test")
	strategy := newRedisStrategy("redis-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newRedisAppRef("cache"),
			BackupClassName: "cozy-default",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	completedK8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobNameForBackupJob(backupJob),
			Namespace: backupJob.Namespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      backupJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: backupJob.Namespace,
			},
		},
		Status: batchv1.JobStatus{
			CompletionTime: &now,
			Conditions:     []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	resolved := newRedisResolved("redis-strategy", map[string]string{"bucketName": "redis-bucket"})

	r, _ := newRedisTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy, completedK8sJob))
	ctx := context.Background()

	if _, err := r.reconcileRedis(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileRedis() error = %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseSucceeded {
		t.Errorf("expected phase Succeeded, got %q", updated.Status.Phase)
	}
	if updated.Status.BackupRef == nil || updated.Status.BackupRef.Name == "" {
		t.Fatalf("expected BackupRef set, got %v", updated.Status.BackupRef)
	}

	created := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: backupJob.Namespace, Name: updated.Status.BackupRef.Name}, created); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	if created.Spec.StrategyRef.Name != "redis-strategy" {
		t.Errorf("expected StrategyRef name 'redis-strategy', got %q", created.Spec.StrategyRef.Name)
	}
	if got := created.Spec.DriverMetadata[redisParamPrefix+"bucketName"]; got != "redis-bucket" {
		t.Errorf("expected driverMetadata[parameter/bucketName]=redis-bucket, got %q", got)
	}
	// The Backup must record the exact object its dump was written to, keyed by
	// the BackupJob name so a Plan's runs do not collapse onto one object.
	wantKey := "tenant-test/cache/test-bj.rdb"
	if got := created.Spec.DriverMetadata[redisObjectMetaKey]; got != wantKey {
		t.Errorf("expected recorded object %q, got %q", wantKey, got)
	}
}

// TestRedisObjectKey_UniquePerBackupAndRoundTrips pins the fix for the
// overwrite-on-every-backup defect: each BackupJob mints a distinct object, and
// a restore reads back the object recorded on ITS Backup rather than whatever
// last landed at a shared path.
func TestRedisObjectKey_UniquePerBackupAndRoundTrips(t *testing.T) {
	monday := redisObjectKey("tenant-test", "cache", "cache-plan-0001")
	tuesday := redisObjectKey("tenant-test", "cache", "cache-plan-0002")
	if monday == tuesday {
		t.Fatalf("distinct BackupJobs must mint distinct objects, both got %q", monday)
	}

	// A restore of Monday's Backup must resolve to Monday's object even after
	// Tuesday's backup exists — the read follows the recorded key, not the newest.
	mondayBackup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newRedisAppRef("cache"),
			DriverMetadata: map[string]string{redisObjectMetaKey: monday},
		},
	}
	if got := redisRestoreObjectKey(mondayBackup); got != monday {
		t.Errorf("restore must read the recorded object %q, got %q", monday, got)
	}

	// Defensive fallback for a Backup with no recorded key (none in a released
	// cluster): the legacy fixed name, so it is at least restorable.
	legacy := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-test"},
		Spec:       backupsv1alpha1.BackupSpec{ApplicationRef: newRedisAppRef("cache")},
	}
	if got, want := redisRestoreObjectKey(legacy), "tenant-test/cache/dump.rdb"; got != want {
		t.Errorf("fallback object = %q, want %q", got, want)
	}
}

func TestReconcileRedis_FailsOnJobFailed(t *testing.T) {
	app := newRedisApp("cache", "tenant-test")
	strategy := newRedisStrategy("redis-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newRedisAppRef("cache"),
			BackupClassName: "cozy-default",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	failedK8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobNameForBackupJob(backupJob),
			Namespace: backupJob.Namespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      backupJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: backupJob.Namespace,
			},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "BackoffLimitExceeded"}},
		},
	}

	r, _ := newRedisTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy, failedK8sJob))
	ctx := context.Background()

	if _, err := r.reconcileRedis(ctx, backupJob, newRedisResolved("redis-strategy", nil)); err != nil {
		t.Fatalf("reconcileRedis() error = %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed, got %q", updated.Status.Phase)
	}
	if updated.Status.Message == "" {
		t.Error("expected failure message to be set")
	}
}

func TestReconcileRedis_FailsOnWrongKind(t *testing.T) {
	app := newRedisApp("cache", "tenant-test")
	strategy := newRedisStrategy("redis-strategy")
	badGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  corev1.TypedLocalObjectReference{APIGroup: &badGroup, Kind: "Postgres", Name: "cache"},
			BackupClassName: "cozy-default",
		},
	}

	r, _ := newRedisTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	ctx := context.Background()

	if _, err := r.reconcileRedis(ctx, backupJob, newRedisResolved("redis-strategy", nil)); err != nil {
		t.Fatalf("reconcileRedis() error = %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed for wrong applicationRef.kind, got %q", updated.Status.Phase)
	}
	if want := "applicationRef.kind"; !strings.Contains(updated.Status.Message, want) {
		t.Errorf("expected gate message to contain %q, got %q", want, updated.Status.Message)
	}
}

// TestReconcileRedis_RequeuesWhenAppNotReady pins that an explicit Ready=False on
// the application blocks the dump Job (no Job created) and surfaces a precise
// ApplicationNotReady condition rather than launching a Job that would fail
// connecting.
func TestReconcileRedis_RequeuesWhenAppNotReady(t *testing.T) {
	app := newRedisApp("cache", "tenant-test")
	app.Object["status"] = map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "HelmUpgradeFailed", "message": "still deploying"}},
	}
	strategy := newRedisStrategy("redis-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newRedisAppRef("cache"),
			BackupClassName: "cozy-default",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}

	r, _ := newRedisTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	ctx := context.Background()

	res, err := r.reconcileRedis(ctx, backupJob, newRedisResolved("redis-strategy", nil))
	if err != nil {
		t.Fatalf("reconcileRedis() error = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the app is not ready")
	}

	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no Job while app not ready, got %d", len(jobs.Items))
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Error("expected non-terminal phase while waiting on app readiness")
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
	if cond == nil || cond.Reason != "ApplicationNotReady" {
		t.Errorf("expected Ready=False/ApplicationNotReady condition, got %v", cond)
	}
}

// TestReconcileRedis_RequeuesOnMissingStrategy pins that a not-yet-provisioned
// Redis strategy CR requeues (bootstrap self-heal) rather than failing the
// BackupJob, mirroring the Altinity driver.
func TestReconcileRedis_RequeuesOnMissingStrategy(t *testing.T) {
	app := newRedisApp("cache", "tenant-test")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newRedisAppRef("cache"),
			BackupClassName: "cozy-default",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}

	r, _ := newRedisTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob))
	ctx := context.Background()

	if _, err := r.reconcileRedis(ctx, backupJob, newRedisResolved("missing-strategy", nil)); err != nil {
		t.Fatalf("reconcileRedis() error = %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Error("expected non-terminal phase for a not-yet-provisioned strategy (bootstrap self-heal)")
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
	if cond == nil || cond.Reason != "StrategyNotReady" {
		t.Errorf("expected Ready=False/StrategyNotReady condition, got %v", cond)
	}
}

// TestReconcileRedis_FailsOnUnmappableKind mirrors the restore-path test: an
// applicationRef.kind that passes the Redis gate but has no REST mapping (a CRD
// that isn't installed) must fail the BackupJob terminally, not requeue forever.
func TestReconcileRedis_FailsOnUnmappableKind(t *testing.T) {
	app := newRedisApp("cache", "tenant-test")
	strategy := newRedisStrategy("redis-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newRedisAppRef("cache"),
			BackupClassName: "cozy-default",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}

	r, _ := newRedisTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	r.RESTMapper = &noMatchRESTMapper{
		mockRESTMapper: &mockRESTMapper{},
		gk:             schema.GroupKind{Group: backupsv1alpha1.DefaultApplicationAPIGroup, Kind: "Redis"},
	}
	ctx := context.Background()

	if _, err := r.reconcileRedis(ctx, backupJob, newRedisResolved("redis-strategy", nil)); err != nil {
		t.Fatalf("reconcileRedis() should fail terminally for an unmappable kind, not return a requeue error: %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed for unmappable kind, got %q", updated.Status.Phase)
	}
	if want := "not found or kind not registered"; !strings.Contains(updated.Status.Message, want) {
		t.Errorf("expected message to contain %q, got %q", want, updated.Status.Message)
	}
}

func TestReconcileRedisRestore_CreatesBatchJobInTargetNamespace(t *testing.T) {
	targetApp := newRedisApp("cache-copy", "tenant-test")
	strategy := &strategyv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: "redis-strategy"},
		Spec: strategyv1alpha1.RedisSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "restore",
						Image: "redis-backup:test",
						Args: []string{
							"--app={{ .Release.Name }}",
							"--mode={{ .Mode }}",
							"--source={{ .Backup.ApplicationRef.Name }}",
							"--bucket={{ .Parameters.bucketName }}",
						},
					}},
				},
			},
		},
	}
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "redis-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newRedisAppRef("cache"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.RedisStrategyKind,
				Name:     "redis-strategy",
			},
			TakenAt:        now,
			DriverMetadata: map[string]string{redisParamPrefix + "bucketName": "redis-bucket"},
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: backup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(backupsv1alpha1.DefaultApplicationAPIGroup),
				Kind:     redisAppKind,
				Name:     "cache-copy",
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}

	_, rr := newRedisTestEnv(t, targetApp, clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
	ctx := context.Background()

	if _, err := rr.reconcileRedisRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileRedisRestore() error = %v", err)
	}

	jobs := &batchv1.JobList{}
	if err := rr.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 restore Job, got %d", len(jobs.Items))
	}
	k8sJob := jobs.Items[0]
	if got := k8sJob.Labels[redisLabelMode]; got != redisModeRestore {
		t.Errorf("expected mode label %q, got %q", redisModeRestore, got)
	}
	args := k8sJob.Spec.Template.Spec.Containers[0].Args
	// to-copy: --app renders to the TARGET (cache-copy), --source to the backup's
	// SOURCE applicationRef (cache), so the restore reads the source's prefix.
	want := []string{"--app=cache-copy", "--mode=restore", "--source=cache", "--bucket=redis-bucket"}
	for i, w := range want {
		if i >= len(args) || args[i] != w {
			t.Errorf("rendered restore args[%d]: want %q, got %q", i, w, args)
		}
	}
}

func TestReconcileRedisRestore_CompletesSucceeds(t *testing.T) {
	targetApp := newRedisApp("cache", "tenant-test")
	strategy := newRedisStrategy("redis-strategy")
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "redis-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newRedisAppRef("cache"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.RedisStrategyKind,
				Name:     "redis-strategy",
			},
			TakenAt: now,
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backup.Name}},
		Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
	completedK8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobNameForRestoreJob(restoreJob),
			Namespace: restoreJob.Namespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
			},
		},
		Status: batchv1.JobStatus{
			CompletionTime: &now,
			Conditions:     []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}

	_, rr := newRedisTestEnv(t, targetApp, clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob, completedK8sJob))
	ctx := context.Background()

	if _, err := rr.reconcileRedisRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileRedisRestore() error = %v", err)
	}

	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Errorf("expected phase Succeeded, got %q", updated.Status.Phase)
	}
}

func TestReconcileRedisRestore_FailsOnUnmappableTargetKind(t *testing.T) {
	targetApp := newRedisApp("cache", "tenant-test")
	strategy := newRedisStrategy("redis-strategy")
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "redis-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newRedisAppRef("cache"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.RedisStrategyKind,
				Name:     "redis-strategy",
			},
			TakenAt: now,
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backup.Name}},
		Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}

	_, rr := newRedisTestEnv(t, targetApp, clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
	rr.RESTMapper = &noMatchRESTMapper{
		mockRESTMapper: &mockRESTMapper{},
		gk:             schema.GroupKind{Group: backupsv1alpha1.DefaultApplicationAPIGroup, Kind: "Redis"},
	}
	ctx := context.Background()

	if _, err := rr.reconcileRedisRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileRedisRestore() should fail terminally for an unmappable target kind, not return a requeue error: %v", err)
	}

	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKeyFromObject(restoreJob), updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("expected phase Failed for unmappable target kind, got %q", updated.Status.Phase)
	}
	if want := "kind not registered"; !strings.Contains(updated.Status.Message, want) {
		t.Errorf("expected message to contain %q, got %q", want, updated.Status.Message)
	}
}
