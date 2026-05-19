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

package server

import (
	"strings"
	"testing"
	"time"
)

// validHelmReleaseFlags returns a CozyServerOptions populated with the same
// defaults NewCozyServerOptions sets, so each test case mutates a single
// flag and asserts the resulting validation behaviour in isolation.
func validHelmReleaseFlags() *CozyServerOptions {
	return &CozyServerOptions{
		HelmReleaseInterval:       "5m",
		HelmReleaseRetryInterval:  "30s",
		HelmReleaseInstallTimeout: "10m",
		HelmReleaseUpgradeTimeout: "10m",
		HelmReleaseMaxHistory:     5,
	}
}

// Pins that each malformed/non-positive duration and a negative MaxHistory
// is rejected at startup. Without this, a regression that swaps `<` for
// `<=` on the MaxHistory branch — breaking the documented "0 means
// unlimited" contract — or that drops a `return err` line on any of the
// four duration branches would ship green.
func TestCozyServerOptions_ParseAndValidateHelmReleaseFlags_Rejects(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(o *CozyServerOptions)
		wantSubs string
	}{
		{
			name:     "interval zero",
			mutate:   func(o *CozyServerOptions) { o.HelmReleaseInterval = "0s" },
			wantSubs: "--helmrelease-interval",
		},
		{
			name:     "interval negative",
			mutate:   func(o *CozyServerOptions) { o.HelmReleaseInterval = "-5m" },
			wantSubs: "--helmrelease-interval",
		},
		{
			name:     "interval malformed",
			mutate:   func(o *CozyServerOptions) { o.HelmReleaseInterval = "5x" },
			wantSubs: "--helmrelease-interval",
		},
		{
			name:     "retry-interval empty",
			mutate:   func(o *CozyServerOptions) { o.HelmReleaseRetryInterval = "" },
			wantSubs: "--helmrelease-retry-interval",
		},
		{
			name:     "install-timeout zero",
			mutate:   func(o *CozyServerOptions) { o.HelmReleaseInstallTimeout = "0s" },
			wantSubs: "--helmrelease-install-timeout",
		},
		{
			name:     "upgrade-timeout malformed",
			mutate:   func(o *CozyServerOptions) { o.HelmReleaseUpgradeTimeout = "tenminutes" },
			wantSubs: "--helmrelease-upgrade-timeout",
		},
		{
			name:     "max-history negative",
			mutate:   func(o *CozyServerOptions) { o.HelmReleaseMaxHistory = -1 },
			wantSubs: "--helmrelease-max-history must be >= 0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := validHelmReleaseFlags()
			tc.mutate(o)
			_, err := o.parseAndValidateHelmReleaseFlags()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubs) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSubs)
			}
		})
	}
}

// Pins the happy path: valid flag strings parse to the expected
// time.Duration values and MaxHistory=0 is accepted (the documented
// "unlimited per Helm semantics" branch).
func TestCozyServerOptions_ParseAndValidateHelmReleaseFlags_Accepts(t *testing.T) {
	cases := []struct {
		name           string
		o              *CozyServerOptions
		wantInterval   time.Duration
		wantRetry      time.Duration
		wantInstall    time.Duration
		wantUpgrade    time.Duration
		wantMaxHistory int
	}{
		{
			name:           "production defaults",
			o:              validHelmReleaseFlags(),
			wantInterval:   5 * time.Minute,
			wantRetry:      30 * time.Second,
			wantInstall:    10 * time.Minute,
			wantUpgrade:    10 * time.Minute,
			wantMaxHistory: 5,
		},
		{
			name: "max-history zero accepted as unlimited",
			o: func() *CozyServerOptions {
				o := validHelmReleaseFlags()
				o.HelmReleaseMaxHistory = 0
				return o
			}(),
			wantInterval:   5 * time.Minute,
			wantRetry:      30 * time.Second,
			wantInstall:    10 * time.Minute,
			wantUpgrade:    10 * time.Minute,
			wantMaxHistory: 0,
		},
		{
			name: "asymmetric install/upgrade timeouts both honored",
			o: func() *CozyServerOptions {
				o := validHelmReleaseFlags()
				o.HelmReleaseInstallTimeout = "8m"
				o.HelmReleaseUpgradeTimeout = "12m"
				return o
			}(),
			wantInterval:   5 * time.Minute,
			wantRetry:      30 * time.Second,
			wantInstall:    8 * time.Minute,
			wantUpgrade:    12 * time.Minute,
			wantMaxHistory: 5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := tc.o.parseAndValidateHelmReleaseFlags()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.interval != tc.wantInterval {
				t.Errorf("interval = %v, want %v", v.interval, tc.wantInterval)
			}
			if v.retryInterval != tc.wantRetry {
				t.Errorf("retryInterval = %v, want %v", v.retryInterval, tc.wantRetry)
			}
			if v.installTimeout != tc.wantInstall {
				t.Errorf("installTimeout = %v, want %v", v.installTimeout, tc.wantInstall)
			}
			if v.upgradeTimeout != tc.wantUpgrade {
				t.Errorf("upgradeTimeout = %v, want %v", v.upgradeTimeout, tc.wantUpgrade)
			}
			if v.maxHistory != tc.wantMaxHistory {
				t.Errorf("maxHistory = %d, want %d", v.maxHistory, tc.wantMaxHistory)
			}
		})
	}
}

// Pins that NewCozyServerOptions seeds defaults that pass validation, so
// `cozystack-api ...` with no HelmRelease flags overridden does not fail
// at startup. Guards against a future change to defaults that accidentally
// violates the validator.
func TestNewCozyServerOptions_DefaultsValidate(t *testing.T) {
	o := NewCozyServerOptions(nil, nil)
	if _, err := o.parseAndValidateHelmReleaseFlags(); err != nil {
		t.Fatalf("default options must validate, got: %v", err)
	}
}
