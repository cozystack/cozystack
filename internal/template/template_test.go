package template

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTemplate_PodTemplateSpec(t *testing.T) {
	original := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-pod",
			Labels: map[string]string{
				"app": "demo",
			},
			Annotations: map[string]string{
				"note": "hello",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "{{ .Release.Name }}",
					Image: "nginx:1.21",
					Args:  []string{"--flag={{ .Values.value }}"},
					Env: []corev1.EnvVar{
						{
							Name:  "FOO",
							Value: "{{ .Release.Namespace }}",
						},
					},
				},
			},
		},
	}
	templateContext := map[string]any{
		"Release": map[string]any{
			"Name":      "foo",
			"Namespace": "notdefault",
		},
		"Values": map[string]any{
			"value": 3,
		},
	}
	reference := *original.DeepCopy()
	reference.Spec.Containers[0].Name = "foo"
	reference.Spec.Containers[0].Args[0] = "--flag=3"
	reference.Spec.Containers[0].Env[0].Value = "notdefault"
	got, err := Template(&original, templateContext)
	if err != nil {
		t.Fatalf("Template returned error: %v", err)
	}
	b1, err := json.Marshal(reference)
	t.Logf("reference:\n%s", string(b1))
	if err != nil {
		t.Fatalf("failed to marshal reference value: %v", err)
	}
	b2, err := json.Marshal(got)
	t.Logf("got:\n%s", string(b2))
	if err != nil {
		t.Fatalf("failed to marshal transformed value: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("transformed value not equal to reference value, expected: %s, got: %s", string(b1), string(b2))
	}
}
