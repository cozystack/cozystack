// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
