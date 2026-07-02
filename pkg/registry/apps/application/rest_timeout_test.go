package application

import (
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// Default HelmRelease global durations the api server applies after flag
// parsing. Tests use these so the fixture exercises the same code path the
// production binary takes — i.e. global timeouts are always non-zero.
const (
	testGlobalInstallTimeout = 10 * time.Minute
	testGlobalUpgradeTimeout = 10 * time.Minute
)

// newRESTForTimeout builds a REST struct focused on the per-Application
// HelmInstallTimeout annotation override path. Global HelmRelease* defaults
// are populated with production-shaped values so the unset-annotation case
// exercises "global default applies". The full spec contract is covered by
// rest_helmrelease_spec_test.go.
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
			HelmReleaseInterval:       5 * time.Minute,
			HelmReleaseRetryInterval:  30 * time.Second,
			HelmReleaseInstallTimeout: testGlobalInstallTimeout,
			HelmReleaseUpgradeTimeout: testGlobalUpgradeTimeout,
			HelmReleaseMaxHistory:     5,
			HelmInstallTimeout:        helmInstallTimeout,
		},
	}
}

// Table-driven: every Application kind carries a per-CRD HelmRelease wait
// budget. The Kubernetes kind's parent chart contains CAPI/Kamaji resources
// whose admin-kubeconfig Secret is provisioned asynchronously, so its
// ApplicationDefinition sets release.cozystack.io/helm-install-timeout=15m
// (or longer). Other kinds leave the annotation unset and inherit the
// global default. The test must cover both paths: a kind with the override
// set and one without.
func TestConvertApplicationToHelmRelease_AppliesReleaseConfigTimeout(t *testing.T) {
	cases := []struct {
		name        string
		kind        string
		prefix      string
		configured  time.Duration
		wantInstall time.Duration
		wantUpgrade time.Duration
	}{
		{
			name:        "Kubernetes kind with 15m override beats global defaults",
			kind:        "Kubernetes",
			prefix:      "kubernetes-",
			configured:  15 * time.Minute,
			wantInstall: 15 * time.Minute,
			wantUpgrade: 15 * time.Minute,
		},
		{
			// Fictional kind on purpose: the test is about the unset path
			// regardless of which real Application kind ends up needing a
			// timeout override. Using a real kind name would create false
			// coupling to that Application's ApplicationDefinition.
			name:        "unrelated kind without override inherits global defaults",
			kind:        "PlaceholderKindForDefaults",
			prefix:      "placeholder-",
			configured:  0,
			wantInstall: testGlobalInstallTimeout,
			wantUpgrade: testGlobalUpgradeTimeout,
		},
		{
			name:        "arbitrary future kind with 20m override gets 20m",
			kind:        "TalosCluster",
			prefix:      "talos-",
			configured:  20 * time.Minute,
			wantInstall: 20 * time.Minute,
			wantUpgrade: 20 * time.Minute,
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

			if hr.Spec.Install.Timeout == nil || hr.Spec.Install.Timeout.Duration != tc.wantInstall {
				t.Errorf("Spec.Install.Timeout = %v, want %v", hr.Spec.Install.Timeout, tc.wantInstall)
			}
			if hr.Spec.Upgrade.Timeout == nil || hr.Spec.Upgrade.Timeout.Duration != tc.wantUpgrade {
				t.Errorf("Spec.Upgrade.Timeout = %v, want %v", hr.Spec.Upgrade.Timeout, tc.wantUpgrade)
			}

			// Strategy must be RetryOnFailure with Remediation kept nil:
			// retries are driven solely by Strategy.RetryInterval, the
			// same single retry path cozystack-operator's
			// PackageReconciler uses. Re-introducing
			// Remediation{Retries: -1} "for safety" would add a second,
			// conflicting retry mechanism and break operator parity.
			if hr.Spec.Install.Strategy == nil ||
				hr.Spec.Install.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
				t.Errorf("Spec.Install.Strategy must be RetryOnFailure, got %+v", hr.Spec.Install.Strategy)
			}
			if hr.Spec.Install.Remediation != nil {
				t.Errorf("Spec.Install.Remediation must be nil, got %+v", hr.Spec.Install.Remediation)
			}
			if hr.Spec.Upgrade.Strategy == nil ||
				hr.Spec.Upgrade.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
				t.Errorf("Spec.Upgrade.Strategy must be RetryOnFailure, got %+v", hr.Spec.Upgrade.Strategy)
			}
			if hr.Spec.Upgrade.Remediation != nil {
				t.Errorf("Spec.Upgrade.Remediation must be nil, got %+v", hr.Spec.Upgrade.Remediation)
			}
		})
	}
}

// HelmUpgradeTimeout (the per-Application release.cozystack.io/helm-upgrade-timeout
// annotation override) overrides only Upgrade.Timeout, and wins over the
// Upgrade.Timeout value HelmInstallTimeout would otherwise apply. This lets a
// kind carry an asymmetric budget — e.g. a short install but a long upgrade.
func TestConvertApplicationToHelmRelease_UpgradeTimeoutAnnotation(t *testing.T) {
	cases := []struct {
		name           string
		installTimeout time.Duration // helm-install-timeout annotation
		upgradeTimeout time.Duration // helm-upgrade-timeout annotation
		wantInstall    time.Duration
		wantUpgrade    time.Duration
	}{
		{
			name:           "upgrade override alone leaves install on the global default",
			installTimeout: 0,
			upgradeTimeout: 20 * time.Minute,
			wantInstall:    testGlobalInstallTimeout,
			wantUpgrade:    20 * time.Minute,
		},
		{
			name:           "upgrade override wins over the install-annotation copy",
			installTimeout: 15 * time.Minute,
			upgradeTimeout: 25 * time.Minute,
			wantInstall:    15 * time.Minute,
			wantUpgrade:    25 * time.Minute,
		},
		{
			name:           "install override alone still sets both (regression guard)",
			installTimeout: 15 * time.Minute,
			upgradeTimeout: 0,
			wantInstall:    15 * time.Minute,
			wantUpgrade:    15 * time.Minute,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRESTForTimeout("Example", "example-", tc.installTimeout)
			r.releaseConfig.HelmUpgradeTimeout = tc.upgradeTimeout
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
			if hr.Spec.Install.Timeout == nil || hr.Spec.Install.Timeout.Duration != tc.wantInstall {
				t.Errorf("Spec.Install.Timeout = %v, want %v", hr.Spec.Install.Timeout, tc.wantInstall)
			}
			if hr.Spec.Upgrade.Timeout == nil || hr.Spec.Upgrade.Timeout.Duration != tc.wantUpgrade {
				t.Errorf("Spec.Upgrade.Timeout = %v, want %v", hr.Spec.Upgrade.Timeout, tc.wantUpgrade)
			}
		})
	}
}
