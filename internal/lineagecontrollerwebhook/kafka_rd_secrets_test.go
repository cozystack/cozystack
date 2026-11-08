package lineagecontrollerwebhook

import (
	"context"
	"os"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

// TestKafkaRDSecrets_KafkaUserCredentialsAreTenantResources runs the Secrets
// the Strimzi User Operator writes for a Kafka instance through the Kafka
// ApplicationDefinition's selectors, as computeLabels does. A user's password
// Secret must be a tenant resource, or the tenant-secret API hides the
// credentials external clients need. A selector whose label value carried a
// template, such as strimzi.io/cluster: kafka-{{ .name }}, never matched,
// since only resourceNames are rendered.
func TestKafkaRDSecrets_KafkaUserCredentialsAreTenantResources(t *testing.T) {
	raw, err := os.ReadFile("../../packages/system/kafka-rd/cozyrds/kafka.yaml")
	if err != nil {
		t.Fatalf("read kafka ApplicationDefinition: %v", err)
	}
	var appDef cozyv1alpha1.ApplicationDefinition
	if err := yaml.Unmarshal(raw, &appDef); err != nil {
		t.Fatalf("parse kafka ApplicationDefinition: %v", err)
	}

	// The context computeLabels builds for a Secret owned by the Kafka
	// application "events" in tenant-test.
	templateContext := map[string]string{"kind": "kafka", "name": "events", "namespace": "tenant-test"}

	cases := []struct {
		name    string
		objName string
		labels  map[string]string
		want    bool
	}{
		{
			name:    "a KafkaUser's generated credentials",
			objName: "kafka-events-app",
			labels: map[string]string{
				"strimzi.io/cluster":           "kafka-events",
				"strimzi.io/kind":              "KafkaUser",
				"app.kubernetes.io/instance":   "app",
				"app.kubernetes.io/managed-by": "strimzi-user-operator",
			},
			want: true,
		},
		{
			name:    "the clients CA certificate",
			objName: "kafka-events-clients-ca-cert",
			labels:  map[string]string{"strimzi.io/cluster": "kafka-events", "strimzi.io/kind": "Kafka"},
			want:    true,
		},
		{
			name:    "the clients CA private key",
			objName: "kafka-events-clients-ca",
			labels:  map[string]string{"strimzi.io/cluster": "kafka-events", "strimzi.io/kind": "Kafka"},
			want:    false,
		},
		{
			name:    "the broker certificates",
			objName: "kafka-events-kafka-brokers",
			labels:  map[string]string{"strimzi.io/cluster": "kafka-events", "strimzi.io/kind": "Kafka"},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchResourceToExcludeInclude(context.Background(), tc.objName, templateContext, tc.labels, &appDef.Spec.Secrets)
			if got != tc.want {
				t.Errorf("tenant resource = %v, want %v", got, tc.want)
			}
		})
	}
}
