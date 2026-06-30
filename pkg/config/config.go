/*
Copyright 2024 The Cozystack Authors.

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

package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// HelmInstallTimeoutAnnotation is the ApplicationDefinition metadata
// annotation key that overrides the Flux HelmRelease Install.Timeout and
// Upgrade.Timeout for a given Application kind.
const HelmInstallTimeoutAnnotation = "release.cozystack.io/helm-install-timeout"

// HelmUpgradeTimeoutAnnotation is the ApplicationDefinition metadata
// annotation key that overrides only the Flux HelmRelease Upgrade.Timeout
// for a given Application kind. When set it takes precedence over the
// Upgrade.Timeout value HelmInstallTimeoutAnnotation would otherwise apply,
// letting a kind keep a short install budget but a longer upgrade budget
// (or vice versa).
const HelmUpgradeTimeoutAnnotation = "release.cozystack.io/helm-upgrade-timeout"

// HelmInstallDisableWaitAnnotation is the ApplicationDefinition metadata
// annotation key that sets HelmReleaseSpec.Install.DisableWait and
// Upgrade.DisableWait to true for a given Application kind. Use when the
// parent chart emits child HelmReleases that cannot become Ready during
// the parent's own install (e.g. the Kubernetes Application emits
// in-tenant addon HelmReleases that have no worker nodes to schedule on
// until the worker MachineSets come up, plus a main-phase talos-reconcile
// Job that produces the TalosConfigTemplate those MachineSets clone from).
// Without DisableWait the helm-controller blocks on the addon HelmReleases
// becoming Ready, which cannot happen during install, so the release never
// settles. DisableWait lets it settle while the addon HelmReleases and the
// reconcile Job converge asynchronously.
const HelmInstallDisableWaitAnnotation = "release.cozystack.io/helm-install-disable-wait"

// helmTimeoutPattern mirrors the CRD validation pattern used by Flux
// helm-controller on HelmReleaseSpec.Install.Timeout (ms/s/m/h units only).
// time.ParseDuration accepts ns/us/µs, but Flux rejects them - parsing here
// with the same shape avoids feeding the controller a value it will later
// reject at webhook time. See
// github.com/fluxcd/helm-controller/api/v2 HelmReleaseSpec.Install.Timeout
// in the go module cache.
var helmTimeoutPattern = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`)

// ParseHelmTimeoutAnnotation parses the value of a Flux HelmRelease timeout
// annotation (release.cozystack.io/helm-install-timeout or
// release.cozystack.io/helm-upgrade-timeout). The empty string is treated as
// "unset" and returns (0, nil) so callers can leave the corresponding
// ReleaseConfig field zeroed and let flux defaults apply. Values accepted by
// time.ParseDuration but rejected by Flux (ns/us/µs) return a helpful
// error instead of silently parsing and failing later at HelmRelease
// admission.
func ParseHelmTimeoutAnnotation(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	if !helmTimeoutPattern.MatchString(raw) {
		return 0, fmt.Errorf("must match %s (Flux accepts ms/s/m/h units only), got %q",
			helmTimeoutPattern, raw)
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("time.ParseDuration(%q): %w", raw, err)
	}
	return d, nil
}

// ParsePositiveDuration parses raw as a time.Duration and rejects malformed
// or non-positive values. Flux HelmRelease fields (Interval, Timeout,
// RetryInterval) require strictly positive durations, so a misconfigured
// flag must fail fast at startup rather than propagating into every HR.
// Shared between cozystack-operator and cozystack-api so both paths reject
// the same set of inputs at startup.
func ParsePositiveDuration(flagName, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s=%q: %w", flagName, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be > 0 (got %q)", flagName, raw)
	}
	return d, nil
}

// ParseHelmInstallDisableWaitAnnotation parses the value of the
// release.cozystack.io/helm-install-disable-wait annotation. Accepts
// "true" or "false" (case-insensitive); empty returns (false, nil) so
// callers can leave HelmInstallDisableWait zero and let flux defaults
// apply.
func ParseHelmInstallDisableWaitAnnotation(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return false, nil
	case strings.EqualFold(raw, "true"):
		return true, nil
	case strings.EqualFold(raw, "false"):
		return false, nil
	default:
		return false, fmt.Errorf("must be \"true\" or \"false\", got %q", raw)
	}
}

// ResourceConfig represents the structure of the configuration file.
type ResourceConfig struct {
	Resources []Resource `yaml:"resources"`
}

// Resource describes an individual resource.
type Resource struct {
	Application ApplicationConfig `yaml:"application"`
	Release     ReleaseConfig     `yaml:"release"`
}

// ApplicationConfig contains the application settings.
type ApplicationConfig struct {
	Kind          string   `yaml:"kind"`
	Singular      string   `yaml:"singular"`
	Plural        string   `yaml:"plural"`
	ShortNames    []string `yaml:"shortNames"`
	OpenAPISchema string   `yaml:"openAPISchema"`
}

// ReleaseConfig contains the release settings.
type ReleaseConfig struct {
	Prefix   string            `yaml:"prefix"`
	Labels   map[string]string `yaml:"labels"`
	ChartRef ChartRefConfig    `yaml:"chartRef"`
	// Placement selects which plane the generated HelmRelease targets.
	// Empty or "ManagementPlane" deploys into the tenant namespace on the
	// management cluster (default). "ComputePlane" injects spec.kubeConfig
	// pointing at the tenant's ComputePlane admin-kubeconfig Secret, so Flux
	// reconciles the release onto the ComputePlane instead. Populated from the
	// ApplicationDefinition's spec.application.placement at start-up.
	Placement string `yaml:"placement,omitempty"`
	// HelmInstallTimeout is a per-Application override of Install.Timeout
	// and Upgrade.Timeout. When non-zero, it wins over
	// HelmReleaseInstallTimeout / HelmReleaseUpgradeTimeout below.
	// Populated from the release.cozystack.io/helm-install-timeout
	// annotation on the ApplicationDefinition at start-up.
	HelmInstallTimeout time.Duration `yaml:"helmInstallTimeout,omitempty"`
	// HelmUpgradeTimeout is a per-Application override of Upgrade.Timeout
	// only. When non-zero, it wins over both HelmReleaseUpgradeTimeout and
	// the Upgrade.Timeout value HelmInstallTimeout would otherwise apply,
	// so a kind can carry an asymmetric install/upgrade budget. Populated
	// from the release.cozystack.io/helm-upgrade-timeout annotation on the
	// ApplicationDefinition at start-up.
	HelmUpgradeTimeout time.Duration `yaml:"helmUpgradeTimeout,omitempty"`
	// HelmReleaseInterval is the global default for Spec.Interval on
	// HelmReleases generated by cozystack-api. Set from the api server
	// --helmrelease-interval flag; mirrors the cozystack-operator flag of
	// the same name so both HelmRelease-generating paths use the same
	// reconcile cadence.
	HelmReleaseInterval time.Duration `yaml:"helmReleaseInterval,omitempty"`
	// HelmReleaseRetryInterval is the global default for
	// Install/Upgrade.Strategy.RetryInterval. Decoupled from
	// HelmReleaseInterval so failed install/upgrade retries recover fast
	// without polling healthy releases at the same cadence.
	HelmReleaseRetryInterval time.Duration `yaml:"helmReleaseRetryInterval,omitempty"`
	// HelmReleaseInstallTimeout is the global default for
	// Spec.Install.Timeout. Overridden per-Application by HelmInstallTimeout
	// when the annotation is set.
	HelmReleaseInstallTimeout time.Duration `yaml:"helmReleaseInstallTimeout,omitempty"`
	// HelmReleaseUpgradeTimeout is the global default for
	// Spec.Upgrade.Timeout. Overridden per-Application by HelmInstallTimeout
	// when the annotation is set.
	HelmReleaseUpgradeTimeout time.Duration `yaml:"helmReleaseUpgradeTimeout,omitempty"`
	// HelmReleaseMaxHistory is the global default for Spec.MaxHistory.
	// 0 means unlimited per Helm semantics; matches the cozystack-operator
	// flag of the same name. No omitempty: 0 ("unlimited") must survive a
	// round-trip distinct from an unset field if ReleaseConfig is ever
	// marshalled.
	HelmReleaseMaxHistory int `yaml:"helmReleaseMaxHistory"`
	// HelmInstallDisableWait sets HelmReleaseSpec.Install.DisableWait and
	// Upgrade.DisableWait to true for this Application kind. Populated
	// from the release.cozystack.io/helm-install-disable-wait
	// annotation on the ApplicationDefinition at start-up.
	HelmInstallDisableWait bool `yaml:"helmInstallDisableWait,omitempty"`
}

// ChartRefConfig references a Flux source artifact for the Helm chart.
type ChartRefConfig struct {
	Kind      string `yaml:"kind"`
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}
