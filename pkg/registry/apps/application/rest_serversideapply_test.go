package application

import (
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// newRESTForServerSideApply builds a REST focused on the per-Application
// HelmServerSideApply override (release.cozystack.io/helm-server-side-apply).
// ssa==nil leaves the field unset (flux/helm-controller default applies).
func newRESTForServerSideApply(ssa *bool) *REST {
	return &REST{
		kindName: "Postgres",
		releaseConfig: config.ReleaseConfig{
			Prefix: "postgres-",
			ChartRef: config.ChartRefConfig{
				Kind:      "HelmChart",
				Name:      "x",
				Namespace: "cozy-system",
			},
			HelmReleaseInterval:       5 * time.Minute,
			HelmReleaseRetryInterval:  30 * time.Second,
			HelmReleaseInstallTimeout: 10 * time.Minute,
			HelmReleaseUpgradeTimeout: 10 * time.Minute,
			HelmReleaseMaxHistory:     5,
			HelmServerSideApply:       ssa,
		},
	}
}

// This is the codified guard for the postgres read-replica autoscaler's core
// invariant: the tenant Postgres HelmRelease must apply CLIENT-SIDE, under which
// helm-controller patches the CNPG Cluster (a CRD) from the previous-vs-new
// rendered manifest without consulting live state, so the chart's constant
// spec.instances seed yields no patch and KEDA's live /scale value is never
// reverted. The platform's helm-controller v1.5.0
// defaults to server-side apply (which force-owns rendered fields), so the
// postgres ApplicationDefinition carries
// release.cozystack.io/helm-server-side-apply: "false"; this test asserts that
// annotation actually threads into Install.ServerSideApply=false and
// Upgrade.ServerSideApply="disabled" on the emitted HelmRelease — on the SAME
// builder (convertApplicationToHelmRelease) that produces the tenant release,
// not the platform Package builder. If the plumbing is removed, this goes red.
func TestConvertApplicationToHelmRelease_ServerSideApplyOverride(t *testing.T) {
	tru := true
	fls := false
	cases := []struct {
		name        string
		ssa         *bool
		wantInstall *bool                     // Install.ServerSideApply
		wantUpgrade helmv2.ServerSideApplyMode // Upgrade.ServerSideApply
	}{
		{
			name:        "unset leaves the helm-controller default (SSA)",
			ssa:         nil,
			wantInstall: nil,
			wantUpgrade: "",
		},
		{
			name:        "false forces client-side apply (autoscaler requirement)",
			ssa:         &fls,
			wantInstall: &fls,
			wantUpgrade: helmv2.ServerSideApplyDisabled,
		},
		{
			name:        "true forces server-side apply",
			ssa:         &tru,
			wantInstall: &tru,
			wantUpgrade: helmv2.ServerSideApplyEnabled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRESTForServerSideApply(tc.ssa)
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

			gotInstall := hr.Spec.Install.ServerSideApply
			switch {
			case tc.wantInstall == nil && gotInstall != nil:
				t.Errorf("Install.ServerSideApply = %v, want nil (unset)", *gotInstall)
			case tc.wantInstall != nil && gotInstall == nil:
				t.Errorf("Install.ServerSideApply = nil, want %v", *tc.wantInstall)
			case tc.wantInstall != nil && gotInstall != nil && *gotInstall != *tc.wantInstall:
				t.Errorf("Install.ServerSideApply = %v, want %v", *gotInstall, *tc.wantInstall)
			}

			if hr.Spec.Upgrade.ServerSideApply != tc.wantUpgrade {
				t.Errorf("Upgrade.ServerSideApply = %q, want %q", hr.Spec.Upgrade.ServerSideApply, tc.wantUpgrade)
			}
		})
	}
}
