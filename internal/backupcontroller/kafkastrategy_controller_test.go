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

	ctxA := kafkaRenderContext(nil, "kafka-test", "tenant-root", kafkaStrategyModeBackup, "run-a", nil, nil)
	uriA, err := renderKafkaArtifactURI(tmpl, ctxA)
	if err != nil {
		t.Fatalf("renderKafkaArtifactURI: %v", err)
	}
	if uriA != "s3://bkt/tenant-root/kafka-test/run-a/kafka-topics.json" {
		t.Fatalf("uriA = %q", uriA)
	}

	ctxB := kafkaRenderContext(nil, "kafka-test", "tenant-root", kafkaStrategyModeBackup, "run-b", nil, nil)
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
		Spec: strategyv1alpha1.KafkaSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "kafka-backup", Image: "kafka:test", Args: []string{"--mode={{ .Mode }}"}}},
		}}},
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
