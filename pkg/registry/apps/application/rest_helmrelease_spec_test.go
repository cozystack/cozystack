/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package application

import (
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// Mirrors the operator-side TestBuildHelmReleaseSpec
// (internal/operator/package_reconciler_test.go) so the api-side
// HelmRelease shape stays in lockstep with cozystack-operator. The two
// HelmRelease-generating paths must populate Interval, MaxHistory,
// Strategy, RetryInterval, and Install/Upgrade.Timeout identically; drift
// between them is the failure mode this test guards against.
func TestConvertApplicationToHelmRelease_BuildsSpecFromConfig(t *testing.T) {
	r := &REST{
		kindName: "Postgres",
		releaseConfig: config.ReleaseConfig{
			Prefix: "postgres-",
			ChartRef: config.ChartRefConfig{
				Kind:      "HelmChart",
				Name:      "postgres",
				Namespace: "cozy-system",
			},
			HelmReleaseInterval:       42 * time.Second,
			HelmReleaseRetryInterval:  17 * time.Second,
			HelmReleaseInstallTimeout: 11 * time.Minute,
			HelmReleaseUpgradeTimeout: 13 * time.Minute,
			HelmReleaseMaxHistory:     7,
		},
	}
	app := &appsv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant-root"},
	}

	hr, err := r.convertApplicationToHelmRelease(app)
	if err != nil {
		t.Fatalf("convertApplicationToHelmRelease returned error: %v", err)
	}

	if hr.Spec.Interval.Duration != 42*time.Second {
		t.Errorf("Interval = %v, want 42s", hr.Spec.Interval.Duration)
	}
	if hr.Spec.MaxHistory == nil {
		t.Fatal("MaxHistory is nil, want pointer to 7")
	}
	if *hr.Spec.MaxHistory != 7 {
		t.Errorf("MaxHistory = %d, want 7", *hr.Spec.MaxHistory)
	}

	if hr.Spec.ChartRef == nil {
		t.Fatal("ChartRef is nil")
	}
	if hr.Spec.ChartRef.Kind != "HelmChart" {
		t.Errorf("ChartRef.Kind = %q, want HelmChart", hr.Spec.ChartRef.Kind)
	}
	if hr.Spec.ChartRef.Name != "postgres" {
		t.Errorf("ChartRef.Name = %q, want postgres", hr.Spec.ChartRef.Name)
	}
	if hr.Spec.ChartRef.Namespace != "cozy-system" {
		t.Errorf("ChartRef.Namespace = %q, want cozy-system", hr.Spec.ChartRef.Namespace)
	}

	if hr.Spec.Install == nil {
		t.Fatal("Install is nil")
	}
	if hr.Spec.Install.Timeout == nil || hr.Spec.Install.Timeout.Duration != 11*time.Minute {
		t.Errorf("Install.Timeout = %v, want 11m", hr.Spec.Install.Timeout)
	}
	if hr.Spec.Install.Strategy == nil {
		t.Fatal("Install.Strategy is nil")
	}
	if hr.Spec.Install.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
		t.Errorf("Install.Strategy.Name = %q, want %q", hr.Spec.Install.Strategy.Name, helmv2.ActionStrategyRetryOnFailure)
	}
	if hr.Spec.Install.Strategy.RetryInterval == nil || hr.Spec.Install.Strategy.RetryInterval.Duration != 17*time.Second {
		t.Errorf("Install.Strategy.RetryInterval = %v, want 17s", hr.Spec.Install.Strategy.RetryInterval)
	}
	// Remediation must remain nil: helm-controller's XValidation rule
	// rejects Strategy.Name=RetryOnFailure with RetryInterval set
	// alongside a Remediation entry, so a future "for safety"
	// re-introduction of Remediation{Retries: -1} would silently break
	// every HR.
	if hr.Spec.Install.Remediation != nil {
		t.Errorf("Install.Remediation = %+v, want nil", hr.Spec.Install.Remediation)
	}

	if hr.Spec.Upgrade == nil {
		t.Fatal("Upgrade is nil")
	}
	if hr.Spec.Upgrade.Timeout == nil || hr.Spec.Upgrade.Timeout.Duration != 13*time.Minute {
		t.Errorf("Upgrade.Timeout = %v, want 13m", hr.Spec.Upgrade.Timeout)
	}
	if hr.Spec.Upgrade.Strategy == nil {
		t.Fatal("Upgrade.Strategy is nil")
	}
	if hr.Spec.Upgrade.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
		t.Errorf("Upgrade.Strategy.Name = %q, want %q", hr.Spec.Upgrade.Strategy.Name, helmv2.ActionStrategyRetryOnFailure)
	}
	if hr.Spec.Upgrade.Strategy.RetryInterval == nil || hr.Spec.Upgrade.Strategy.RetryInterval.Duration != 17*time.Second {
		t.Errorf("Upgrade.Strategy.RetryInterval = %v, want 17s", hr.Spec.Upgrade.Strategy.RetryInterval)
	}
	if hr.Spec.Upgrade.Remediation != nil {
		t.Errorf("Upgrade.Remediation = %+v, want nil", hr.Spec.Upgrade.Remediation)
	}
}

// Pins that MaxHistory=0 (unlimited history per Helm semantics) survives the
// conversion — i.e. is set as a non-nil pointer to 0 rather than dropped or
// replaced with a default. Mirrors the operator-side
// TestBuildHelmReleaseSpecZeroMaxHistory.
func TestConvertApplicationToHelmRelease_ZeroMaxHistory(t *testing.T) {
	r := &REST{
		kindName: "Postgres",
		releaseConfig: config.ReleaseConfig{
			Prefix:                   "postgres-",
			HelmReleaseInterval:      30 * time.Second,
			HelmReleaseRetryInterval: 30 * time.Second,
			HelmReleaseMaxHistory:    0,
		},
	}
	app := &appsv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "tenant-root"},
	}

	hr, err := r.convertApplicationToHelmRelease(app)
	if err != nil {
		t.Fatalf("convertApplicationToHelmRelease returned error: %v", err)
	}
	if hr.Spec.MaxHistory == nil {
		t.Fatal("MaxHistory is nil for HelmReleaseMaxHistory=0; want pointer to 0")
	}
	if *hr.Spec.MaxHistory != 0 {
		t.Errorf("MaxHistory = %d, want 0", *hr.Spec.MaxHistory)
	}
}

// HelmInstallTimeout (the per-Application annotation override) wins over the
// global HelmReleaseInstallTimeout / HelmReleaseUpgradeTimeout defaults. This
// is the contract that lets ApplicationDefinitions like Kubernetes (with its
// long Kamaji bootstrap) opt out of the global default without operators
// having to widen the global flag for every kind.
//
// The asymmetric-globals case pins that the override wipes both install and
// upgrade timeouts to the same value, even when the globals differ — a
// regression that only assigned one would slip through if the test only
// checked symmetric globals.
func TestConvertApplicationToHelmRelease_PerAppTimeoutOverridesGlobal(t *testing.T) {
	cases := []struct {
		name           string
		globalInstall  time.Duration
		globalUpgrade  time.Duration
		override       time.Duration
		wantInstall    time.Duration
		wantUpgrade    time.Duration
	}{
		{
			name:          "symmetric globals, override wins",
			globalInstall: 10 * time.Minute,
			globalUpgrade: 10 * time.Minute,
			override:      30 * time.Minute,
			wantInstall:   30 * time.Minute,
			wantUpgrade:   30 * time.Minute,
		},
		{
			name:          "asymmetric globals, override wipes both to same value",
			globalInstall: 8 * time.Minute,
			globalUpgrade: 12 * time.Minute,
			override:      30 * time.Minute,
			wantInstall:   30 * time.Minute,
			wantUpgrade:   30 * time.Minute,
		},
		{
			name:          "asymmetric globals, no override keeps each side independent",
			globalInstall: 8 * time.Minute,
			globalUpgrade: 12 * time.Minute,
			override:      0,
			wantInstall:   8 * time.Minute,
			wantUpgrade:   12 * time.Minute,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &REST{
				kindName: "Kubernetes",
				releaseConfig: config.ReleaseConfig{
					Prefix:                    "kubernetes-",
					HelmReleaseInterval:       5 * time.Minute,
					HelmReleaseRetryInterval:  30 * time.Second,
					HelmReleaseInstallTimeout: tc.globalInstall,
					HelmReleaseUpgradeTimeout: tc.globalUpgrade,
					HelmReleaseMaxHistory:     5,
					HelmInstallTimeout:        tc.override,
				},
			}
			app := &appsv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "tenant-root"},
			}

			hr, err := r.convertApplicationToHelmRelease(app)
			if err != nil {
				t.Fatalf("convertApplicationToHelmRelease returned error: %v", err)
			}
			if hr.Spec.Install.Timeout == nil || hr.Spec.Install.Timeout.Duration != tc.wantInstall {
				t.Errorf("Install.Timeout = %v, want %v", hr.Spec.Install.Timeout, tc.wantInstall)
			}
			if hr.Spec.Upgrade.Timeout == nil || hr.Spec.Upgrade.Timeout.Duration != tc.wantUpgrade {
				t.Errorf("Upgrade.Timeout = %v, want %v", hr.Spec.Upgrade.Timeout, tc.wantUpgrade)
			}
		})
	}
}
