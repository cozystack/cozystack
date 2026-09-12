// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
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
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/kafkatypes"
)

func strp(s string) *string { return &s }

func TestValidateKafkaApplicationRef(t *testing.T) {
	cases := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{"kind Kafka, empty group", corev1.TypedLocalObjectReference{Kind: "Kafka"}, false},
		{"kind Kafka, default group", corev1.TypedLocalObjectReference{Kind: "Kafka", APIGroup: strp("apps.cozystack.io")}, false},
		{"wrong kind", corev1.TypedLocalObjectReference{Kind: "Postgres"}, true},
		{"wrong group", corev1.TypedLocalObjectReference{Kind: "Kafka", APIGroup: strp("kafka.strimzi.io")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateKafkaApplicationRef(tc.ref)
			if tc.wantErr != (err != nil) {
				t.Fatalf("validateKafkaApplicationRef(%+v) err=%v, wantErr=%v", tc.ref, err, tc.wantErr)
			}
		})
	}
}

func TestKafkaClusterName(t *testing.T) {
	if got := kafkaClusterName("foo"); got != "kafka-foo" {
		t.Fatalf("kafkaClusterName(foo) = %q, want kafka-foo", got)
	}
}

func TestKafkaNotReadyMessage(t *testing.T) {
	ready := &kafkatypes.Kafka{
		ObjectMeta: metav1.ObjectMeta{Name: "kafka-app"},
		Status: kafkatypes.KafkaStatus{Conditions: []metav1.Condition{
			{Type: kafkatypes.ConditionTypeReady, Status: metav1.ConditionTrue},
		}},
	}
	if msg := kafkaNotReadyMessage(ready); msg != "" {
		t.Fatalf("ready cluster: got %q, want empty", msg)
	}

	notReady := &kafkatypes.Kafka{
		ObjectMeta: metav1.ObjectMeta{Name: "kafka-app"},
		Status: kafkatypes.KafkaStatus{Conditions: []metav1.Condition{
			{Type: kafkatypes.ConditionTypeReady, Status: metav1.ConditionFalse, Message: "brokers pending"},
		}},
	}
	msg := kafkaNotReadyMessage(notReady)
	if !strings.Contains(msg, "Ready=False") || !strings.Contains(msg, "brokers pending") {
		t.Fatalf("not-ready cluster: got %q, want Ready=False + message", msg)
	}

	missing := &kafkatypes.Kafka{ObjectMeta: metav1.ObjectMeta{Name: "kafka-app"}}
	if msg := kafkaNotReadyMessage(missing); !strings.Contains(msg, "no Ready condition") {
		t.Fatalf("missing condition: got %q, want 'no Ready condition'", msg)
	}
}

func TestKafkaStrategyParameters_RoundTrip(t *testing.T) {
	b := &backupsv1alpha1.Backup{
		Spec: backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{
			kafkaStrategyParamPrefix + "topics": "orders",
			kafkaStrategyParamPrefix + "":       "dropped-empty-key",
			"unrelated":                         "ignored",
		}},
	}
	got := kafkaStrategyParameters(b)
	if len(got) != 1 || got["topics"] != "orders" {
		t.Fatalf("kafkaStrategyParameters = %v, want {topics: orders}", got)
	}
}

func TestRenderKafkaArtifactURI(t *testing.T) {
	tmpl := "s3://bkt/{{ .Release.Namespace }}/{{ .Release.Name }}/{{ .BackupName }}/kafka-topics.json"

	ctxA := kafkaRenderContext(nil, "kafka-test", "tenant-root", kafkaStrategyModeBackup, "run-a", "", "", nil, nil)
	uriA, err := renderKafkaArtifactURI(tmpl, ctxA)
	if err != nil {
		t.Fatalf("renderKafkaArtifactURI: %v", err)
	}
	if uriA != "s3://bkt/tenant-root/kafka-test/run-a/kafka-topics.json" {
		t.Fatalf("uriA = %q", uriA)
	}

	ctxB := kafkaRenderContext(nil, "kafka-test", "tenant-root", kafkaStrategyModeBackup, "run-b", "", "", nil, nil)
	uriB, _ := renderKafkaArtifactURI(tmpl, ctxB)
	if uriA == uriB {
		t.Fatalf("distinct BackupName must yield distinct key: %q == %q", uriA, uriB)
	}

	if uri, err := renderKafkaArtifactURI("", ctxA); err != nil || uri != "" {
		t.Fatalf("empty template: uri=%q err=%v, want empty/nil", uri, err)
	}
}

// TestReconcileKafka_RejectsWrongKind covers the applicationRef Kind gate on the
// BackupJob path: a non-Kafka application terminates the BackupJob as Failed
// rather than running a Job against it.
func TestReconcileKafka_RejectsWrongKind(t *testing.T) {
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: strp("apps.cozystack.io"), Kind: "Postgres", Name: "pg",
			},
			BackupClassName: "cozy-default",
		},
	}
	c := newBackupJobTestClient(t, bj)
	r := &BackupJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	}); err != nil {
		t.Fatalf("reconcileKafka returned error: %v", err)
	}

	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("get bj: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("wrong-kind BackupJob phase = %q, want Failed", got.Status.Phase)
	}
}

func newKafkaApp(name, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   backupsv1alpha1.DefaultApplicationAPIGroup,
		Version: "v1alpha1",
		Kind:    "Kafka",
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

func newKafkaTestEnv(t *testing.T, app *unstructured.Unstructured, builder *clientfake.ClientBuilder) (*BackupJobReconciler, *RestoreJobReconciler) {
	t.Helper()
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)
	_ = kafkatypes.AddToScheme(testScheme)

	gvr := schema.GroupVersionResource{Group: backupsv1alpha1.DefaultApplicationAPIGroup, Version: "v1alpha1", Resource: "kafkas"}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		testScheme, map[schema.GroupVersionResource]string{gvr: "KafkaList"}, app)
	restMapper := &mockRESTMapper{mapping: &meta.RESTMapping{
		Resource: gvr, GroupVersionKind: app.GroupVersionKind(), Scope: meta.RESTScopeNamespace,
	}}
	c := builder.WithScheme(testScheme).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()
	return &BackupJobReconciler{Client: c, Interface: dynamicClient, RESTMapper: restMapper, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)},
		&RestoreJobReconciler{Client: c, Interface: dynamicClient, RESTMapper: restMapper, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)}
}

func newKafkaStrategy(name string) *strategyv1alpha1.Kafka {
	return &strategyv1alpha1.Kafka{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: strategyv1alpha1.KafkaSpec{
			ArtifactURITemplate: "s3://bkt/{{ .Release.Namespace }}/{{ .Release.Name }}/{{ .BackupName }}/kafka-metadata.txt",
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers:    []corev1.Container{{Name: "kafka-backup", Image: "{{ .ClientImage }}", Args: []string{"--mode={{ .Mode }}", "--uri={{ .ArtifactURI }}"}}},
			}},
		},
	}
}

func kafkaAppRef(name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{APIGroup: strp(backupsv1alpha1.DefaultApplicationAPIGroup), Kind: "Kafka", Name: name}
}

// TestReconcileKafka_WaitsForNotReadyCluster: while the Strimzi Kafka cluster is
// not Ready the BackupJob must stay out of Succeeded (Ready=False precondition,
// requeue) and create no batch Job — the precondition is one of the driver's
// three reasons to exist, and a mutation dropping it must fail here.
func TestReconcileKafka_WaitsForNotReadyCluster(t *testing.T) {
	now := metav1.Now()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src"), BackupClassName: "cozy-default"},
		// Preset StartedAt so reconcile skips the first-pass bookkeeping requeue
		// and reaches the readiness gate.
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	cluster := &kafkatypes.Kafka{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "kafka-src"},
		Status: kafkatypes.KafkaStatus{Conditions: []metav1.Condition{
			{Type: kafkatypes.ConditionTypeReady, Status: metav1.ConditionFalse, Reason: "Creating", Message: "brokers starting"},
		}},
	}
	r, _ := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(bj, newKafkaStrategy("cozy-default-kafka"), cluster))

	res, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	})
	if err != nil {
		t.Fatalf("reconcileKafka: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected a requeue while the cluster is not Ready, got %+v", res)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("get bj: %v", err)
	}
	if got.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded {
		t.Fatalf("BackupJob reached Succeeded while cluster not Ready")
	}
	jobs := &batchv1.JobList{}
	if err := r.List(context.Background(), jobs, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no batch Job while cluster not Ready, got %d", len(jobs.Items))
	}
}

// TestReconcileKafkaRestore_RejectsWrongTargetKind: a to-copy restore whose
// target is not a Kafka application terminates the RestoreJob as Failed.
func TestReconcileKafkaRestore_RejectsWrongTargetKind(t *testing.T) {
	pgGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef:            corev1.LocalObjectReference{Name: "bk"},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{APIGroup: &pgGroup, Kind: "Postgres", Name: "pg"},
		},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: kafkaAppRef("src"),
			StrategyRef:    corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
		},
	}
	c := newRestoreJobTestClient(t, rj, backup)
	r := &RestoreJobReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.reconcileKafkaRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcileKafkaRestore: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("get rj: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("wrong-target-kind RestoreJob phase = %q, want Failed", got.Status.Phase)
	}
}

// restoreJobRunning builds a RestoreJob already past its first-reconcile
// bookkeeping (StartedAt preset), so a test reaches the gates below it.
func restoreJobRunning(name, namespace string, target *corev1.TypedLocalObjectReference) *backupsv1alpha1.RestoreJob {
	now := metav1.Now()
	return &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef:            corev1.LocalObjectReference{Name: "bk"},
			TargetApplicationRef: target,
		},
		Status: backupsv1alpha1.RestoreJobStatus{StartedAt: &now, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
	}
}

// TestReconcileKafkaRestore_ToCopy pins the to-copy happy path AND the
// target-field merge: the restore resolves the TARGET (targetApplicationRef),
// not the source. Only "dst" is registered, so reaching Succeeded proves the
// TargetApplicationRef.Name merge was observed - ignoring it would resolve the
// absent source "src" and fail.
func TestReconcileKafkaRestore_ToCopy(t *testing.T) {
	rj := restoreJobRunning("rj", "tenant",
		&corev1.TypedLocalObjectReference{APIGroup: strp(backupsv1alpha1.DefaultApplicationAPIGroup), Kind: "Kafka", Name: "dst"})
	backup := kafkaBackup("bk", "tenant") // source "src", artifact URI recorded
	completed := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj-restore"},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
	_, r := newKafkaTestEnv(t, newKafkaApp("dst", "tenant"),
		clientfake.NewClientBuilder().WithObjects(rj, backup, newKafkaStrategy("cozy-default-kafka"),
			readyKafkaCluster("kafka-dst", "tenant"), kafkaBrokerPod("kafka-dst", "tenant", "quay.io/strimzi/kafka:test"), completed))

	if _, err := r.reconcileKafkaRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcileKafkaRestore: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("get rj: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Fatalf("to-copy restore phase = %q, want Succeeded (target merge unobserved?)", got.Status.Phase)
	}
}

// TestReconcileKafkaRestore_NoRecordedArtifactURI pins that a Backup with no
// recorded object fails FAST, before the readiness gate - not after waiting out
// the deadline and reporting a misleading readiness timeout. No cluster is
// seeded, so the old ordering would requeue instead of failing.
func TestReconcileKafkaRestore_NoRecordedArtifactURI(t *testing.T) {
	rj := restoreJobRunning("rj", "tenant", nil)
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: kafkaAppRef("src"),
			StrategyRef:    corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
		},
	}
	_, r := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(rj, backup, newKafkaStrategy("cozy-default-kafka")))

	if _, err := r.reconcileKafkaRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcileKafkaRestore: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("get rj: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("no-artifact restore phase = %q, want Failed fast (not a readiness timeout)", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "artifact URI") {
		t.Fatalf("message = %q, want it to name the missing artifact URI", got.Status.Message)
	}
}

// TestReconcileKafkaRestore_TargetClusterNotReady pins that while the target
// Kafka cluster is not Ready the restore requeues and creates no Job.
func TestReconcileKafkaRestore_TargetClusterNotReady(t *testing.T) {
	rj := restoreJobRunning("rj", "tenant", nil)
	backup := kafkaBackup("bk", "tenant")
	notReady := &kafkatypes.Kafka{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "kafka-src"},
		Status: kafkatypes.KafkaStatus{Conditions: []metav1.Condition{
			{Type: kafkatypes.ConditionTypeReady, Status: metav1.ConditionFalse, Reason: "Creating", Message: "brokers starting"},
		}},
	}
	_, r := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(rj, backup, newKafkaStrategy("cozy-default-kafka"), notReady))

	res, err := r.reconcileKafkaRestore(context.Background(), rj, backup)
	if err != nil {
		t.Fatalf("reconcileKafkaRestore: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected a requeue while the target cluster is not Ready, got %+v", res)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("get rj: %v", err)
	}
	if got.Status.Phase == backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Fatal("restore reached Succeeded while target cluster not Ready")
	}
	jobs := &batchv1.JobList{}
	if err := r.List(context.Background(), jobs, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no restore Job while cluster not Ready, got %d", len(jobs.Items))
	}
}

// TestReconcileKafkaRestore_TargetNotFound pins that a restore whose target
// application is not registered terminates as Failed rather than requeuing.
func TestReconcileKafkaRestore_TargetNotFound(t *testing.T) {
	rj := restoreJobRunning("rj", "tenant", nil) // target defaults to the source "src"
	backup := kafkaBackup("bk", "tenant")
	// Seed a different app so the target "src" resolves NotFound.
	_, r := newKafkaTestEnv(t, newKafkaApp("other", "tenant"),
		clientfake.NewClientBuilder().WithObjects(rj, backup, newKafkaStrategy("cozy-default-kafka")))

	if _, err := r.reconcileKafkaRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcileKafkaRestore: %v", err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("get rj: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("missing-target restore phase = %q, want Failed", got.Status.Phase)
	}
}

// TestCreateKafkaBackupArtifact_StampsScope pins that every produced Backup
// carries strategy.backups.cozystack.io/scope=topic-metadata, so `kubectl
// describe backup` and any consumer see the narrow scope. Dropping the stamp
// turns this red.
func TestCreateKafkaBackupArtifact_StampsScope(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	c := clientfake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&backupsv1alpha1.Backup{}).Build()
	r := &BackupJobReconciler{Client: c, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)}

	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src")},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	}
	backup, err := r.createKafkaBackupArtifact(context.Background(), bj, resolved, "s3://bkt/tenant/src/bj/kafka-metadata.txt", "quay.io/strimzi/kafka:test@sha256:deadbeef")
	if err != nil {
		t.Fatalf("createKafkaBackupArtifact: %v", err)
	}
	if got := backup.Spec.DriverMetadata[kafkaScopeKey]; got != kafkaScopeValue {
		t.Fatalf("scope stamp = %q, want %q (driverMetadata=%v)", got, kafkaScopeValue, backup.Spec.DriverMetadata)
	}
	// The resolved broker image is recorded so cleanup can reuse it once the
	// source cluster is gone.
	if got := backup.Spec.DriverMetadata[kafkaStrategyClientImageKey]; got != "quay.io/strimzi/kafka:test@sha256:deadbeef" {
		t.Fatalf("recorded client image = %q, want the resolved broker image (driverMetadata=%v)", got, backup.Spec.DriverMetadata)
	}
}

// TestCreateKafkaBackupArtifact_RejectsDifferentApp pins the adoption guard: a
// same-named Backup left over for a different application is not reported as this
// run's success (mirrors the RabbitMQ driver).
func TestCreateKafkaBackupArtifact_RejectsDifferentApp(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)

	// A retained Backup named "bj" that describes application "other".
	stale := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupSpec{ApplicationRef: kafkaAppRef("other")},
	}
	c := clientfake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&backupsv1alpha1.Backup{}).WithObjects(stale).Build()
	r := &BackupJobReconciler{Client: c, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)}

	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src")},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	}
	if _, err := r.createKafkaBackupArtifact(context.Background(), bj, resolved, "s3://bkt/tenant/src/bj/kafka-metadata.txt", "quay.io/strimzi/kafka:test@sha256:deadbeef"); err == nil {
		t.Fatal("expected an error adopting a same-named Backup for a different application, got nil")
	} else if !strings.Contains(err.Error(), "different application") {
		t.Fatalf("error = %v, want it to mention a different application", err)
	}
}

// TestCleanupOnDelete_Kafka_RoutesToKafkaCleanup pins the Backup-delete dispatch
// case: a Kafka Backup must route to cleanupKafkaBackup (which spawns the delete
// Job and requeues), not fall through to the Velero default that would release
// the object un-deleted. Dropping the `case KafkaStrategyKind` turns this red.
func TestCleanupOnDelete_Kafka_RoutesToKafkaCleanup(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)

	backup := kafkaBackup("kafka-src", "tenant-test")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}
	c := clientfake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&batchv1.Job{}).
		WithObjects(ns, backup, newKafkaStrategy("cozy-default-kafka")).Build()
	r := &BackupReconciler{Client: c, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)}

	res, err := r.cleanupOnDelete(context.Background(), backup)
	if err != nil {
		t.Fatalf("cleanupOnDelete: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected a requeue while the Kafka delete Job runs; a fall-through to the Velero default would release the object un-deleted")
	}
	jobs := &batchv1.JobList{}
	if err := c.List(context.Background(), jobs, client.InNamespace("tenant-test")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 1 || jobs.Items[0].Name != "kafka-src-cleanup" {
		t.Fatalf("expected one kafka-src-cleanup Job from the Kafka dispatch route, got %+v", jobs.Items)
	}
}

// TestReconcileKafka_FailsAfterReadinessDeadline pins the deadline: a BackupJob
// whose cluster never becomes Ready must terminate as Failed once
// kafkaDefaultBackupDeadline elapses, not requeue forever. Neutralising the
// deadline comparison turns this red.
func TestReconcileKafka_FailsAfterReadinessDeadline(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-(kafkaDefaultBackupDeadline + time.Minute)))
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src"), BackupClassName: "cozy-default"},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &old, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	// App present, but the Strimzi Kafka cluster CR absent → never Ready.
	r, _ := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(bj, newKafkaStrategy("cozy-default-kafka")))

	if _, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	}); err != nil {
		t.Fatalf("reconcileKafka: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("get bj: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("expected Failed after the readiness deadline, got %q", got.Status.Phase)
	}
}

func getKafkaJob(t *testing.T, c client.Client, namespace, name string) *batchv1.Job {
	t.Helper()
	job := &batchv1.Job{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, job); err != nil {
		t.Fatalf("get job %s/%s: %v", namespace, name, err)
	}
	return job
}

func TestKafkaRunDeadline(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   time.Duration
	}{
		{"unset falls back to default", nil, kafkaDefaultBackupDeadline},
		{"valid override honoured", map[string]string{kafkaBackupTimeoutParam: "2h"}, 2 * time.Hour},
		{"below floor falls back", map[string]string{kafkaBackupTimeoutParam: "10s"}, kafkaDefaultBackupDeadline},
		{"unparseable falls back", map[string]string{kafkaBackupTimeoutParam: "nope"}, kafkaDefaultBackupDeadline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kafkaRunDeadline(tc.params); got != tc.want {
				t.Fatalf("kafkaRunDeadline(%v) = %s, want %s", tc.params, got, tc.want)
			}
		})
	}
}

// TestReconcileKafka_BoundsRunWithActiveDeadline pins that the run is bounded at
// the Job layer: the backup Job the controller creates carries
// activeDeadlineSeconds, so Kubernetes fails an unschedulable or wedged Job as
// DeadlineExceeded and kills its pod before the script's final upload - rather
// than a controller wall-clock that marks the BackupJob Failed while the Job
// runs on and orphans its object. Dropping setKafkaJobDeadline turns this red.
func TestReconcileKafka_BoundsRunWithActiveDeadline(t *testing.T) {
	now := metav1.Now()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src"), BackupClassName: "cozy-default"},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	r, _ := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(bj, newKafkaStrategy("cozy-default-kafka"),
			readyKafkaCluster("kafka-src", "tenant"), kafkaBrokerPod("kafka-src", "tenant", "quay.io/strimzi/kafka:test")))

	if _, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	}); err != nil {
		t.Fatalf("reconcileKafka: %v", err)
	}
	job := getKafkaJob(t, r.Client, "tenant", "bj-backup")
	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("backup Job must carry activeDeadlineSeconds to bound the run at the Job layer")
	}
	if got, want := *job.Spec.ActiveDeadlineSeconds, int64(kafkaDefaultBackupDeadline/time.Second); got != want {
		t.Fatalf("activeDeadlineSeconds = %d, want %d", got, want)
	}
}

// TestReconcileKafka_BackupTimeoutParameterOverridesDeadline pins that the
// backupTimeout BackupClass parameter widens the Job's activeDeadlineSeconds, so
// a cluster whose export legitimately runs long is not capped at the default.
func TestReconcileKafka_BackupTimeoutParameterOverridesDeadline(t *testing.T) {
	now := metav1.Now()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src"), BackupClassName: "cozy-default"},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	r, _ := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(bj, newKafkaStrategy("cozy-default-kafka"),
			readyKafkaCluster("kafka-src", "tenant"), kafkaBrokerPod("kafka-src", "tenant", "quay.io/strimzi/kafka:test")))

	if _, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
		Parameters:  map[string]string{kafkaBackupTimeoutParam: "2h"},
	}); err != nil {
		t.Fatalf("reconcileKafka: %v", err)
	}
	job := getKafkaJob(t, r.Client, "tenant", "bj-backup")
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != int64((2*time.Hour)/time.Second) {
		t.Fatalf("activeDeadlineSeconds = %v, want %d from backupTimeout=2h", job.Spec.ActiveDeadlineSeconds, int64((2*time.Hour)/time.Second))
	}
}

// TestReconcileKafka_FailsWhenJobFails pins that a backup Job reported Failed -
// which is how Kubernetes marks a run that exceeds activeDeadlineSeconds -
// terminates the BackupJob as Failed rather than requeuing in Running forever.
func TestReconcileKafka_FailsWhenJobFails(t *testing.T) {
	now := metav1.Now()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src"), BackupClassName: "cozy-default"},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	failedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-backup"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "Job was active longer than specified deadline"},
		}},
	}
	r, _ := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(bj, newKafkaStrategy("cozy-default-kafka"),
			readyKafkaCluster("kafka-src", "tenant"), kafkaBrokerPod("kafka-src", "tenant", "quay.io/strimzi/kafka:test"), failedJob))

	if _, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	}); err != nil {
		t.Fatalf("reconcileKafka: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("get bj: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("expected Failed once the Job reported Failed, got %q", got.Status.Phase)
	}
}

// podListErrClient forces PodList to fail, to simulate a transient apiserver
// error while resolving the broker image.
type podListErrClient struct {
	client.Client
	err error
}

func (c podListErrClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*corev1.PodList); ok {
		return c.err
	}
	return c.Client.List(ctx, list, opts...)
}

// TestReconcileKafka_RequeuesTransientBrokerImageError pins that a transient
// List error while resolving the broker image requeues (returns an error) rather
// than terminating the whole backup - only a Ready cluster with no listable
// broker is terminal.
func TestReconcileKafka_RequeuesTransientBrokerImageError(t *testing.T) {
	now := metav1.Now()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src"), BackupClassName: "cozy-default"},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	r, _ := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(bj, newKafkaStrategy("cozy-default-kafka"),
			readyKafkaCluster("kafka-src", "tenant")))
	r.Client = podListErrClient{Client: r.Client, err: apierrors.NewInternalError(errors.New("apiserver unavailable"))}

	_, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	})
	if err == nil {
		t.Fatal("expected a requeue error on a transient broker List failure")
	}
	got := &backupsv1alpha1.BackupJob{}
	if gerr := r.Get(context.Background(), client.ObjectKeyFromObject(bj), got); gerr != nil {
		t.Fatalf("get bj: %v", gerr)
	}
	if got.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatal("a transient List error must not terminate the backup as Failed")
	}
}

// kafkaBackup builds a Backup fixture that has already recorded its metadata
// object, for the cleanup path (whose delete is keyed on status.artifact.uri).
func kafkaBackup(name, namespace string) *backupsv1alpha1.Backup {
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: kafkaAppRef("src"),
			StrategyRef:    corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
			DriverMetadata: map[string]string{
				kafkaScopeKey:               kafkaScopeValue,
				kafkaStrategyClientImageKey: "quay.io/strimzi/kafka:test@sha256:deadbeef",
			},
		},
		Status: backupsv1alpha1.BackupStatus{
			Phase:    backupsv1alpha1.BackupPhaseReady,
			Artifact: &backupsv1alpha1.BackupArtifact{URI: "s3://bkt/" + namespace + "/src/" + name + "/kafka-metadata.txt"},
		},
	}
}

// TestCleanupKafkaBackup_SpawnsDeleteJob pins that deleting a Backup spawns an
// ownerless, self-cleaning Job that deletes the recorded object in cleanup mode
// (with the URI injected), so a retention-pruned Plan does not leak objects -
// the same contract the Backup cleanup dispatcher routes KafkaStrategyKind to.
func TestCleanupKafkaBackup_SpawnsDeleteJob(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)

	backup := kafkaBackup("kafka-src", "tenant-test")
	strategy := newKafkaStrategy("cozy-default-kafka")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}
	c := clientfake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&batchv1.Job{}).
		WithObjects(ns, backup, strategy).Build()
	// Empty CredentialsConfig: ProjectBackupCredentials is a no-op when disabled.
	r := &BackupReconciler{Client: c, Scheme: testScheme, Recorder: record.NewFakeRecorder(10)}
	ctx := context.Background()

	res, err := r.cleanupKafkaBackup(ctx, backup)
	if err != nil {
		t.Fatalf("cleanupKafkaBackup() error = %v", err)
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
	if job.Name != "kafka-src-cleanup" {
		t.Errorf("job name = %q, want kafka-src-cleanup", job.Name)
	}
	if len(job.OwnerReferences) != 0 {
		t.Errorf("cleanup Job must be ownerless so it survives Backup deletion, got %d ownerRefs", len(job.OwnerReferences))
	}
	if job.Labels[kafkaStrategyLabelMode] != kafkaStrategyModeCleanup {
		t.Errorf("mode label = %q, want %q", job.Labels[kafkaStrategyLabelMode], kafkaStrategyModeCleanup)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || job.Spec.ActiveDeadlineSeconds == nil {
		t.Error("expected TTLSecondsAfterFinished + ActiveDeadlineSeconds on the cleanup Job")
	}
	args := job.Spec.Template.Spec.Containers[0].Args
	foundMode, foundURI := false, false
	for _, a := range args {
		if a == "--mode=cleanup" {
			foundMode = true
		}
		if a == "--uri="+backup.Status.Artifact.URI {
			foundURI = true
		}
	}
	if !foundMode {
		t.Errorf("expected the pod rendered in cleanup mode (--mode=cleanup), got args %v", args)
	}
	if !foundURI {
		t.Errorf("expected the recorded artifact URI injected into the cleanup pod, got args %v", args)
	}
	// Cleanup runs through the image the backup recorded (the source broker may be
	// gone), injected via the .ClientImage placeholder.
	if img := job.Spec.Template.Spec.Containers[0].Image; img != backup.Spec.DriverMetadata[kafkaStrategyClientImageKey] {
		t.Errorf("cleanup Job image = %q, want the recorded client image %q", img, backup.Spec.DriverMetadata[kafkaStrategyClientImageKey])
	}

	// Once the delete Job completes, cleanup finishes (zero Result) and reaps the
	// Job - so the Backup is only released after the object is gone.
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, &job); err != nil {
		t.Fatalf("update job status: %v", err)
	}
	res, err = r.cleanupKafkaBackup(ctx, backup)
	if err != nil {
		t.Fatalf("cleanupKafkaBackup() second call error = %v", err)
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

func readyKafkaCluster(name, namespace string) *kafkatypes.Kafka {
	return &kafkatypes.Kafka{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status: kafkatypes.KafkaStatus{Conditions: []metav1.Condition{
			{Type: kafkatypes.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: "Ready"},
		}},
	}
}

// kafkaBrokerPod builds a Strimzi broker pod the way the driver locates it: by
// the cluster label, with the image on a container named "kafka".
func kafkaBrokerPod(cluster, namespace, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      cluster + "-broker-0",
			Labels:    map[string]string{kafkaBrokerClusterLabel: cluster},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: kafkaBrokerContainerName, Image: image}}},
	}
}

// TestReconcileKafka_ResolvesBrokerImage pins that the backup Job runs the target
// broker's own image (resolved from a live broker pod), not a chart pin. Dropping
// the resolver leaves the container image the unrendered ".ClientImage"
// placeholder, turning this red.
func TestReconcileKafka_ResolvesBrokerImage(t *testing.T) {
	const brokerImage = "quay.io/strimzi/kafka:0.45.1-kafka-3.9.1@sha256:abc"
	now := metav1.Now()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src"), BackupClassName: "cozy-default"},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	r, _ := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(bj, newKafkaStrategy("cozy-default-kafka"),
			readyKafkaCluster("kafka-src", "tenant"), kafkaBrokerPod("kafka-src", "tenant", brokerImage)))

	if _, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	}); err != nil {
		t.Fatalf("reconcileKafka: %v", err)
	}
	jobs := &batchv1.JobList{}
	if err := r.List(context.Background(), jobs, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected one backup Job, got %d", len(jobs.Items))
	}
	if img := jobs.Items[0].Spec.Template.Spec.Containers[0].Image; img != brokerImage {
		t.Fatalf("backup Job image = %q, want the resolved broker image %q", img, brokerImage)
	}
}

// TestReconcileKafka_FailsWhenNoBrokerImage pins the fail-closed behaviour: a
// Ready cluster with no resolvable broker image terminates the BackupJob Failed
// with a legible reason and creates no Job, rather than running one with an
// empty/placeholder image.
func TestReconcileKafka_FailsWhenNoBrokerImage(t *testing.T) {
	now := metav1.Now()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec:       backupsv1alpha1.BackupJobSpec{ApplicationRef: kafkaAppRef("src"), BackupClassName: "cozy-default"},
		Status:     backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	// Ready cluster, but no broker pod to resolve the image from.
	r, _ := newKafkaTestEnv(t, newKafkaApp("src", "tenant"),
		clientfake.NewClientBuilder().WithObjects(bj, newKafkaStrategy("cozy-default-kafka"),
			readyKafkaCluster("kafka-src", "tenant")))

	if _, err := r.reconcileKafka(context.Background(), bj, &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.KafkaStrategyKind, Name: "cozy-default-kafka"},
	}); err != nil {
		t.Fatalf("reconcileKafka: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(bj), got); err != nil {
		t.Fatalf("get bj: %v", err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("phase = %q, want Failed when the client image cannot be resolved", got.Status.Phase)
	}
	jobs := &batchv1.JobList{}
	if err := r.List(context.Background(), jobs, client.InNamespace("tenant")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no batch Job when the image cannot be resolved, got %d", len(jobs.Items))
	}
}
