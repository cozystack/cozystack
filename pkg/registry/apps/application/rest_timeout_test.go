package application

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

func TestConvertApplicationToHelmRelease_SetsInstallAndUpgradeTimeout(t *testing.T) {
	r := &REST{
		releaseConfig: config.ReleaseConfig{
			Prefix: "kubernetes-",
			ChartRef: config.ChartRefConfig{
				Kind:      "HelmChart",
				Name:      "kubernetes",
				Namespace: "cozy-system",
			},
		},
	}

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
		t.Fatal("Spec.Install.Timeout must be set to cover async admin-kubeconfig provisioning")
	}
	if hr.Spec.Install.Timeout.Duration < 15*time.Minute {
		t.Errorf("Spec.Install.Timeout must be >= 15m (cold bootstrap budget), got %v", hr.Spec.Install.Timeout.Duration)
	}

	if hr.Spec.Upgrade == nil {
		t.Fatal("Spec.Upgrade must not be nil")
	}
	if hr.Spec.Upgrade.Timeout == nil {
		t.Fatal("Spec.Upgrade.Timeout must be set to cover async admin-kubeconfig provisioning")
	}
	if hr.Spec.Upgrade.Timeout.Duration < 15*time.Minute {
		t.Errorf("Spec.Upgrade.Timeout must be >= 15m (cold bootstrap budget), got %v", hr.Spec.Upgrade.Timeout.Duration)
	}
}
