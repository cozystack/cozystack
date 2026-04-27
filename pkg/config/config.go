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
	"time"
)

// HelmInstallTimeoutAnnotation is the ApplicationDefinition metadata
// annotation key that overrides the Flux HelmRelease Install.Timeout and
// Upgrade.Timeout for a given Application kind.
const HelmInstallTimeoutAnnotation = "release.cozystack.io/helm-install-timeout"

// helmTimeoutPattern mirrors the CRD validation pattern used by Flux
// helm-controller on HelmReleaseSpec.Install.Timeout (ms/s/m/h units only).
// time.ParseDuration accepts ns/us/µs, but Flux rejects them - parsing here
// with the same shape avoids feeding the controller a value it will later
// reject at webhook time. See
// github.com/fluxcd/helm-controller/api/v2 HelmReleaseSpec.Install.Timeout
// in the go module cache.
var helmTimeoutPattern = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`)

// ParseHelmInstallTimeoutAnnotation parses the value of the
// release.cozystack.io/helm-install-timeout annotation. The empty string is
// treated as "unset" and returns (0, nil) so callers can leave
// HelmInstallTimeout zeroed and let flux defaults apply. Values accepted by
// time.ParseDuration but rejected by Flux (ns/us/µs) return a helpful
// error instead of silently parsing and failing later at HelmRelease
// admission.
func ParseHelmInstallTimeoutAnnotation(raw string) (time.Duration, error) {
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
	// HelmInstallTimeout overrides the Flux HelmRelease Install.Timeout and
	// Upgrade.Timeout for this Application kind. When zero, flux defaults
	// apply. Populated from the
	// release.cozystack.io/helm-install-timeout annotation on the
	// ApplicationDefinition at start-up.
	HelmInstallTimeout time.Duration `yaml:"helmInstallTimeout,omitempty"`
}

// ChartRefConfig references a Flux source artifact for the Helm chart.
type ChartRefConfig struct {
	Kind      string `yaml:"kind"`
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}
