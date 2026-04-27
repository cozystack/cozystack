package application

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

func newRESTForTimeout(kind, prefix string, helmInstallTimeout time.Duration) *REST {
	return &REST{
		kindName: kind,
		releaseConfig: config.ReleaseConfig{
			Prefix: prefix,
			ChartRef: config.ChartRefConfig{
				Kind:      "HelmChart",
				Name:      "x",
				Namespace: "cozy-system",
			},
			HelmInstallTimeout: helmInstallTimeout,
		},
	}
}

// Table-driven: every Application kind carries a per-CRD HelmRelease wait
// budget. The Kubernetes kind's parent chart contains CAPI/Kamaji resources
// whose admin-kubeconfig Secret is provisioned asynchronously, so its
// ApplicationDefinition sets release.cozystack.io/helm-install-timeout=15m
// (or longer). Other kinds leave the annotation unset and keep flux defaults
// so their failed installs remediate on the normal cadence. The test must
// cover both paths: a kind with the timeout set and one without.
func TestConvertApplicationToHelmRelease_AppliesReleaseConfigTimeout(t *testing.T) {
	cases := []struct {
		name       string
		kind       string
		prefix     string
		configured time.Duration
		wantSet    bool
	}{
		{
			name:       "Kubernetes kind with 15m configured gets Install and Upgrade Timeout",
			kind:       "Kubernetes",
			prefix:     "kubernetes-",
			configured: 15 * time.Minute,
			wantSet:    true,
		},
		{
			// Fictional kind on purpose: the test is about the unset path
			// regardless of which real Application kind ends up needing a
			// timeout override. Using a real kind name would create false
			// coupling to that Application's ApplicationDefinition.
			name:       "unrelated kind without configured timeout keeps flux defaults",
			kind:       "PlaceholderKindForDefaults",
			prefix:     "placeholder-",
			configured: 0,
			wantSet:    false,
		},
		{
			name:       "arbitrary future kind with 20m configured gets 20m",
			kind:       "TalosCluster",
			prefix:     "talos-",
			configured: 20 * time.Minute,
			wantSet:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRESTForTimeout(tc.kind, tc.prefix, tc.configured)
			app := &appsv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant-root"},
			}

			hr, err := r.convertApplicationToHelmRelease(app)
			if err != nil {
				t.Fatalf("convertApplicationToHelmRelease returned error: %v", err)
			}

			if hr.Spec.Install == nil || hr.Spec.Upgrade == nil {
				t.Fatalf("Spec.Install/Upgrade must be non-nil")
			}

			if tc.wantSet {
				if hr.Spec.Install.Timeout == nil {
					t.Fatalf("Spec.Install.Timeout must be set when HelmInstallTimeout=%v", tc.configured)
				}
				if hr.Spec.Install.Timeout.Duration != tc.configured {
					t.Errorf("Spec.Install.Timeout = %v, want %v", hr.Spec.Install.Timeout.Duration, tc.configured)
				}
				if hr.Spec.Upgrade.Timeout == nil {
					t.Fatalf("Spec.Upgrade.Timeout must be set when HelmInstallTimeout=%v", tc.configured)
				}
				if hr.Spec.Upgrade.Timeout.Duration != tc.configured {
					t.Errorf("Spec.Upgrade.Timeout = %v, want %v", hr.Spec.Upgrade.Timeout.Duration, tc.configured)
				}
			} else {
				if hr.Spec.Install.Timeout != nil {
					t.Errorf("Spec.Install.Timeout must be nil when HelmInstallTimeout=0, got %v", hr.Spec.Install.Timeout.Duration)
				}
				if hr.Spec.Upgrade.Timeout != nil {
					t.Errorf("Spec.Upgrade.Timeout must be nil when HelmInstallTimeout=0, got %v", hr.Spec.Upgrade.Timeout.Duration)
				}
			}

			if hr.Spec.Install.Remediation == nil || hr.Spec.Install.Remediation.Retries != -1 {
				t.Errorf("Spec.Install.Remediation.Retries must remain -1, got %+v", hr.Spec.Install.Remediation)
			}
			if hr.Spec.Upgrade.Remediation == nil || hr.Spec.Upgrade.Remediation.Retries != -1 {
				t.Errorf("Spec.Upgrade.Remediation.Retries must remain -1, got %+v", hr.Spec.Upgrade.Remediation)
			}
		})
	}
}
