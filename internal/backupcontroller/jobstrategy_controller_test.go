// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
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

// The Job strategy is app-agnostic, so the tests deliberately use a generic
// application kind ("Generic") rather than one of the specialised drivers'
// kinds. This pins the contract that reconcileJob/reconcileJobRestore route
// purely on the resolved StrategyRef and never gate on applicationRef.kind.
const jobStrategyTestKind = "Generic"

// newJobStrategyApp returns an unstructured application the driver's dynamic
// client can serve. The fake client matches GVR exactly, so the
// apiVersion/kind/group must align with what mockRESTMapper returns in
// newJobStrategyTestEnv.
func newJobStrategyApp(name, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   backupsv1alpha1.DefaultApplicationAPIGroup,
		Version: "v1alpha1",
		Kind:    jobStrategyTestKind,
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

func newJobStrategyAppRef(name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{
		APIGroup: stringPtr(backupsv1alpha1.DefaultApplicationAPIGroup),
		Kind:     jobStrategyTestKind,
		Name:     name,
	}
}

// newJobStrategyTestEnv builds the scheme, dynamic client, REST mapper, and
// reconciler harness used across Job strategy unit tests. Mirrors
// newAltinityTestEnv but for a generic application kind.
func newJobStrategyTestEnv(t *testing.T, app *unstructured.Unstructured, builder *clientfake.ClientBuilder) (*BackupJobReconciler, *RestoreJobReconciler) {
	t.Helper()

	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)

	gvr := schema.GroupVersionResource{
		Group:    backupsv1alpha1.DefaultApplicationAPIGroup,
		Version:  "v1alpha1",
		Resource: "generics",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		testScheme,
		map[schema.GroupVersionResource]string{gvr: jobStrategyTestKind + "List"},
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

// newJobStrategy returns a Job strategy whose template exercises every key the
// driver exposes to the template engine: .Release, .Mode, and .Parameters.
func newJobStrategy(name string) *strategyv1alpha1.Job {
	return &strategyv1alpha1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: strategyv1alpha1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "backup",
						Image: "backup-tool:test",
						Args: []string{
							"--app={{ .Release.Name }}",
							"--mode={{ .Mode }}",
							"--bucket={{ .Parameters.bucketName }}",
						},
					}},
				},
			},
		},
	}
}

func newJobStrategyResolved(strategyName string, params map[string]string) *ResolvedBackupConfig {
	return &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
			Kind:     strategyv1alpha1.JobStrategyKind,
			Name:     strategyName,
		},
		Parameters: params,
	}
}

func TestReconcileJob_CreatesBatchJob(t *testing.T) {
	app := newJobStrategyApp("app-test", "tenant-test")
	strategy := newJobStrategy("generic-strategy")

	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newJobStrategyAppRef("app-test"),
			BackupClassName: "generic-backup",
		},
	}
	resolved := newJobStrategyResolved("generic-strategy", map[string]string{"bucketName": "gen-bucket"})

	r, _ := newJobStrategyTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	ctx := context.Background()

	// First reconcile patches Status.StartedAt and requeues without touching
	// the Job - mirrors the Altinity/CNPG pattern that avoids carrying a stale
	// ResourceVersion into the next status write in the same reconcile.
	if _, err := r.reconcileJob(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileJob() first call: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), backupJob); err != nil {
		t.Fatalf("refresh BackupJob after first reconcile: %v", err)
	}
	// Second reconcile materialises the Job once StartedAt is already set.
	if _, err := r.reconcileJob(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileJob() second call: %v", err)
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
	if got := k8sJob.Labels[jobStrategyLabelMode]; got != jobStrategyModeBackup {
		t.Errorf("expected mode label %q, got %q", jobStrategyModeBackup, got)
	}

	containerArgs := k8sJob.Spec.Template.Spec.Containers[0].Args
	wantArgs := []string{"--app=app-test", "--mode=backup", "--bucket=gen-bucket"}
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
		t.Errorf("expected phase Running, got %q", updated.Status.Phase)
	}
	if updated.Status.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
}

func TestReconcileJob_CompletesAndCreatesBackup(t *testing.T) {
	app := newJobStrategyApp("app-test", "tenant-test")
	strategy := newJobStrategy("generic-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newJobStrategyAppRef("app-test"),
			BackupClassName: "generic-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{
			StartedAt: &now,
			Phase:     backupsv1alpha1.BackupJobPhaseRunning,
		},
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
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}
	resolved := newJobStrategyResolved("generic-strategy", map[string]string{"bucketName": "gen-bucket"})

	r, _ := newJobStrategyTestEnv(t, app,
		clientfake.NewClientBuilder().WithObjects(backupJob, strategy, completedK8sJob))
	ctx := context.Background()

	if _, err := r.reconcileJob(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileJob() error = %v", err)
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
	if created.Spec.StrategyRef.Name != "generic-strategy" {
		t.Errorf("expected StrategyRef name 'generic-strategy', got %q", created.Spec.StrategyRef.Name)
	}
	if got := created.Spec.DriverMetadata[jobStrategyParamPrefix+"bucketName"]; got != "gen-bucket" {
		t.Errorf("expected driverMetadata[parameter/bucketName]=gen-bucket, got %q", got)
	}
	// Status.Phase is intentionally NOT asserted: Backup has the status
	// subresource, so the apiserver (and the fake client's
	// WithStatusSubresource registration) drops status on Create. Promotion to
	// Ready is the BackupReconciler's job, not this driver's. See the matching
	// note in createAltinityBackupArtifact.
}

func TestReconcileJob_FailsOnJobFailed(t *testing.T) {
	app := newJobStrategyApp("app-test", "tenant-test")
	strategy := newJobStrategy("generic-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newJobStrategyAppRef("app-test"),
			BackupClassName: "generic-backup",
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
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "BackoffLimitExceeded"},
			},
		},
	}
	resolved := newJobStrategyResolved("generic-strategy", nil)

	r, _ := newJobStrategyTestEnv(t, app,
		clientfake.NewClientBuilder().WithObjects(backupJob, strategy, failedK8sJob))
	ctx := context.Background()

	if _, err := r.reconcileJob(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileJob() error = %v", err)
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

func TestReconcileJob_FailsOnMissingStrategy(t *testing.T) {
	app := newJobStrategyApp("app-test", "tenant-test")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newJobStrategyAppRef("app-test"),
			BackupClassName: "generic-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	// No Job strategy object created: the lookup must fail the BackupJob with a
	// terminal phase rather than erroring out into an infinite backoff.
	resolved := newJobStrategyResolved("missing-strategy", nil)

	r, _ := newJobStrategyTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob))
	ctx := context.Background()

	if _, err := r.reconcileJob(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileJob() error = %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: backupJob.Namespace, Name: backupJob.Name}, updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed for missing strategy, got %q", updated.Status.Phase)
	}
}

func TestReconcileJobRestore_CreatesBatchJobInTargetNamespace(t *testing.T) {
	targetApp := newJobStrategyApp("app-restore", "tenant-test")
	strategy := &strategyv1alpha1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "generic-strategy"},
		Spec: strategyv1alpha1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "restore",
						Image: "backup-tool:test",
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
		ObjectMeta: metav1.ObjectMeta{Name: "gen-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newJobStrategyAppRef("app-test"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.JobStrategyKind,
				Name:     "generic-strategy",
			},
			TakenAt:        now,
			DriverMetadata: map[string]string{jobStrategyParamPrefix + "bucketName": "gen-bucket"},
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: backup.Name},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(backupsv1alpha1.DefaultApplicationAPIGroup),
				Kind:     jobStrategyTestKind,
				Name:     "app-restore",
			},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}

	_, rr := newJobStrategyTestEnv(t, targetApp,
		clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
	ctx := context.Background()

	if _, err := rr.reconcileJobRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileJobRestore() error = %v", err)
	}

	jobs := &batchv1.JobList{}
	if err := rr.List(ctx, jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list batch jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 restore Job, got %d", len(jobs.Items))
	}
	k8sJob := jobs.Items[0]
	if got := k8sJob.Name; got != jobNameForRestoreJob(restoreJob) {
		t.Errorf("expected Job name %q, got %q", jobNameForRestoreJob(restoreJob), got)
	}
	if got := k8sJob.Labels[jobStrategyLabelMode]; got != jobStrategyModeRestore {
		t.Errorf("expected mode label %q, got %q", jobStrategyModeRestore, got)
	}
	args := k8sJob.Spec.Template.Spec.Containers[0].Args
	// --app renders to the restore TARGET (app-restore); --source renders to
	// the backup's SOURCE applicationRef (app-test); --bucket round-trips the
	// parameter snapshot stored on the Backup's DriverMetadata.
	want := []string{"--app=app-restore", "--mode=restore", "--source=app-test", "--bucket=gen-bucket"}
	for i, w := range want {
		if i >= len(args) || args[i] != w {
			t.Errorf("rendered restore args[%d]: want %q, got %q", i, w, args)
		}
	}
}

func TestReconcileJobRestore_CompletesSucceeds(t *testing.T) {
	targetApp := newJobStrategyApp("app-test", "tenant-test")
	strategy := newJobStrategy("generic-strategy")
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "gen-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newJobStrategyAppRef("app-test"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.JobStrategyKind,
				Name:     "generic-strategy",
			},
			TakenAt: now,
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: backup.Name},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
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
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}

	_, rr := newJobStrategyTestEnv(t, targetApp,
		clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob, completedK8sJob))
	ctx := context.Background()

	if _, err := rr.reconcileJobRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileJobRestore() error = %v", err)
	}

	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKey{Namespace: restoreJob.Namespace, Name: restoreJob.Name}, updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Errorf("expected phase Succeeded, got %q", updated.Status.Phase)
	}
	if updated.Status.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

func TestJobStrategyParameters_RoundTrip(t *testing.T) {
	backup := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{
			DriverMetadata: map[string]string{
				jobStrategyParamPrefix + "bucketName": "b1",
				jobStrategyParamPrefix + "endpoint":   "https://s3",
				"job.batch/name":                      "ignored",
				"":                                    "ignored",
			},
		},
	}
	got := jobStrategyParameters(backup)
	if got["bucketName"] != "b1" || got["endpoint"] != "https://s3" {
		t.Errorf("jobStrategyParameters = %v", got)
	}
	if _, ok := got["job.batch/name"]; ok {
		t.Errorf("non-parameter key leaked through: %v", got)
	}
}

// noMatchRESTMapper makes RESTMapping fail with a *meta.NoKindMatchError,
// modelling an applicationRef.kind that has no REST mapping - a typo in the
// kind, or a CRD that isn't installed. It embeds the shared mockRESTMapper so
// the rest of the meta.RESTMapper interface is satisfied; getApplicationUnstructured
// only reaches RESTMapping before bailing out on the error.
type noMatchRESTMapper struct {
	*mockRESTMapper
	gk schema.GroupKind
}

func (m *noMatchRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	return nil, &meta.NoKindMatchError{GroupKind: m.gk}
}

// TestReconcileJob_FailsOnUnmappableKind pins that an applicationRef.kind with
// no REST mapping fails the BackupJob terminally instead of bubbling up a
// non-NotFound error that controller-runtime would requeue forever. Because the
// Job driver removes the kind-validation gate the specialised drivers have, it
// is the path that forwards arbitrary kinds into RESTMapping and must classify
// the resulting NoKindMatchError itself. Without the fix, reconcileJob returns
// the error (failing the t.Fatalf below) and the BackupJob stays in Running.
func TestReconcileJob_FailsOnUnmappableKind(t *testing.T) {
	app := newJobStrategyApp("app-test", "tenant-test")
	strategy := newJobStrategy("generic-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newJobStrategyAppRef("app-test"),
			BackupClassName: "generic-backup",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	resolved := newJobStrategyResolved("generic-strategy", nil)

	r, _ := newJobStrategyTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy))
	r.RESTMapper = &noMatchRESTMapper{
		mockRESTMapper: &mockRESTMapper{},
		gk:             schema.GroupKind{Group: backupsv1alpha1.DefaultApplicationAPIGroup, Kind: "Bogus"},
	}
	ctx := context.Background()

	if _, err := r.reconcileJob(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileJob() should fail the BackupJob terminally for an unmappable kind, not return a requeue error: %v", err)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: backupJob.Namespace, Name: backupJob.Name}, updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("expected phase Failed for unmappable kind, got %q", updated.Status.Phase)
	}
	if updated.Status.Message == "" {
		t.Error("expected failure message to be set")
	}
}

// TestReconcileJobRestore_FailsOnUnmappableTargetKind is the RestoreJob mirror
// of TestReconcileJob_FailsOnUnmappableKind.
func TestReconcileJobRestore_FailsOnUnmappableTargetKind(t *testing.T) {
	targetApp := newJobStrategyApp("app-test", "tenant-test")
	strategy := newJobStrategy("generic-strategy")
	now := metav1.Now()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "gen-backup", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: newJobStrategyAppRef("app-test"),
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(strategyv1alpha1.GroupVersion.Group),
				Kind:     strategyv1alpha1.JobStrategyKind,
				Name:     "generic-strategy",
			},
			TakenAt: now,
		},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: backup.Name},
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}

	_, rr := newJobStrategyTestEnv(t, targetApp,
		clientfake.NewClientBuilder().WithObjects(strategy, backup, restoreJob))
	rr.RESTMapper = &noMatchRESTMapper{
		mockRESTMapper: &mockRESTMapper{},
		gk:             schema.GroupKind{Group: backupsv1alpha1.DefaultApplicationAPIGroup, Kind: "Bogus"},
	}
	ctx := context.Background()

	if _, err := rr.reconcileJobRestore(ctx, restoreJob, backup); err != nil {
		t.Fatalf("reconcileJobRestore() should fail the RestoreJob terminally for an unmappable kind, not return a requeue error: %v", err)
	}

	updated := &backupsv1alpha1.RestoreJob{}
	if err := rr.Get(ctx, client.ObjectKey{Namespace: restoreJob.Namespace, Name: restoreJob.Name}, updated); err != nil {
		t.Fatalf("get restorejob: %v", err)
	}
	if updated.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Errorf("expected phase Failed for unmappable target kind, got %q", updated.Status.Phase)
	}
	if updated.Status.Message == "" {
		t.Error("expected failure message to be set")
	}
}
