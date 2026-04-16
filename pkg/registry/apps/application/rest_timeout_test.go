package application

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

func newRESTForKind(kind, prefix string) *REST {
	return &REST{
		kindName: kind,
		releaseConfig: config.ReleaseConfig{
			Prefix: prefix,
			ChartRef: config.ChartRefConfig{
				Kind:      "HelmChart",
				Name:      "x",
				Namespace: "cozy-system",
			},
		},
	}
}

func TestConvertApplicationToHelmRelease_KubernetesKindGetsLongTimeout(t *testing.T) {
	r := newRESTForKind("Kubernetes", "kubernetes-")
	app := &appsv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant-root"},
	}

	hr, err := r.convertApplicationToHelmRelease(app)
	if err != nil {
		t.Fatalf("convertApplicationToHelmRelease returned error: %v", err)
	}

	if hr.Spec.Install == nil {
		t.Fatal("Spec.Install must not be nil")
	}
	if hr.Spec.Install.Timeout == nil {
		t.Fatal("Spec.Install.Timeout must be set for Kubernetes kind")
	}
	if hr.Spec.Install.Timeout.Duration < 15*time.Minute {
		t.Errorf("Spec.Install.Timeout must be >= 15m for Kubernetes, got %v", hr.Spec.Install.Timeout.Duration)
	}

	if hr.Spec.Upgrade == nil {
		t.Fatal("Spec.Upgrade must not be nil")
	}
	if hr.Spec.Upgrade.Timeout == nil {
		t.Fatal("Spec.Upgrade.Timeout must be set for Kubernetes kind")
	}
	if hr.Spec.Upgrade.Timeout.Duration < 15*time.Minute {
		t.Errorf("Spec.Upgrade.Timeout must be >= 15m for Kubernetes, got %v", hr.Spec.Upgrade.Timeout.Duration)
	}

	if hr.Spec.Install.Remediation == nil || hr.Spec.Install.Remediation.Retries != -1 {
		t.Errorf("Spec.Install.Remediation.Retries must remain -1, got %+v", hr.Spec.Install.Remediation)
	}
	if hr.Spec.Upgrade.Remediation == nil || hr.Spec.Upgrade.Remediation.Retries != -1 {
		t.Errorf("Spec.Upgrade.Remediation.Retries must remain -1, got %+v", hr.Spec.Upgrade.Remediation)
	}
}

func TestConvertApplicationToHelmRelease_NonKubernetesKindKeepsFluxDefaults(t *testing.T) {
	// For Applications whose parent chart has no admin-kubeconfig race
	// (Qdrant, MongoDB, Postgres, etc.), do NOT extend the helm-wait
	// budget - otherwise failed installs would block three times as long
	// before Flux starts remediating.
	r := newRESTForKind("Qdrant", "qdrant-")
	app := &appsv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant-root"},
	}

	hr, err := r.convertApplicationToHelmRelease(app)
	if err != nil {
		t.Fatalf("convertApplicationToHelmRelease returned error: %v", err)
	}

	if hr.Spec.Install != nil && hr.Spec.Install.Timeout != nil {
		t.Errorf("Spec.Install.Timeout must be unset for non-Kubernetes kinds, got %v", hr.Spec.Install.Timeout.Duration)
	}
	if hr.Spec.Upgrade != nil && hr.Spec.Upgrade.Timeout != nil {
		t.Errorf("Spec.Upgrade.Timeout must be unset for non-Kubernetes kinds, got %v", hr.Spec.Upgrade.Timeout.Duration)
	}

	// But remediation must still be -1 across the board.
	if hr.Spec.Install.Remediation == nil || hr.Spec.Install.Remediation.Retries != -1 {
		t.Errorf("Spec.Install.Remediation.Retries must remain -1, got %+v", hr.Spec.Install.Remediation)
	}
	if hr.Spec.Upgrade.Remediation == nil || hr.Spec.Upgrade.Remediation.Retries != -1 {
		t.Errorf("Spec.Upgrade.Remediation.Retries must remain -1, got %+v", hr.Spec.Upgrade.Remediation)
	}
}
