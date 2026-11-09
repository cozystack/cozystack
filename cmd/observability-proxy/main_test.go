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

package main

import (
	"testing"
	"time"
)

func TestParseFlagsDefaults(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.listenAddress != ":8080" || o.probeAddress != ":8081" || o.clusterDomain != "cozy.local" || o.rootTenant != "tenant-root" {
		t.Errorf("contract defaults changed: %+v", o)
	}
	if o.authCacheTTL != 10*time.Second || o.logs.MaxTailResume != 15*time.Minute || o.logs.MaxRange != 7*24*time.Hour {
		t.Errorf("limit defaults changed: %+v", o)
	}
	// Retention and store names of packages/system/monitoring/values.yaml.
	m := o.metrics
	if m.ShortTermStore != "shortterm" || m.LongTermStore != "longterm" || m.ShortTermRetention != 72*time.Hour {
		t.Errorf("metrics store defaults changed: %+v", m)
	}
	if m.Limits.MaxRange != 30*24*time.Hour || m.Limits.MaxPoints != 11000 || m.Limits.MaxTimeout != 30*time.Second {
		t.Errorf("metrics limit defaults changed: %+v", m.Limits)
	}
	if m.RootTenant != o.rootTenant {
		t.Errorf("metrics root tenant = %q, want --root-tenant", m.RootTenant)
	}
}

func TestParseFlagsOverrides(t *testing.T) {
	o, err := parseFlags([]string{"--cluster-domain=example.org", "--logs-max-range=24h", "--max-tails=3"})
	if err != nil {
		t.Fatal(err)
	}
	if o.clusterDomain != "example.org" || o.logs.MaxRange != 24*time.Hour || o.limiter.MaxTails != 3 {
		t.Errorf("overrides not applied: %+v", o)
	}
	// The smallest bucket count and range the step cap can satisfy.
	if _, err := parseFlags([]string{"--logs-max-buckets=4", "--logs-max-range=3s", "--logs-default-range=1s"}); err != nil {
		t.Errorf("smallest satisfiable step cap refused: %v", err)
	}
}

func TestParseFlagsMetricsOverrides(t *testing.T) {
	o, err := parseFlags([]string{
		"--root-tenant=tenant-x", "--metrics-shortterm-store=fast", "--metrics-longterm-store=slow",
		"--metrics-shortterm-retention=24h", "--metrics-max-range=168h", "--metrics-max-points=500", "--metrics-max-timeout=10s",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := o.metrics
	if m.RootTenant != "tenant-x" || m.ShortTermStore != "fast" || m.LongTermStore != "slow" || m.ShortTermRetention != 24*time.Hour ||
		m.Limits.MaxRange != 168*time.Hour || m.Limits.MaxPoints != 500 || m.Limits.MaxTimeout != 10*time.Second {
		t.Errorf("overrides not applied: %+v", m)
	}
}

func TestParseFlagsInvalid(t *testing.T) {
	for _, args := range [][]string{
		{"--root-tenant="},
		{"--cluster-domain="},
		{"--logs-default-range=48h", "--logs-max-range=24h"},
		{"--logs-max-buckets=3"},
		{"--logs-max-range=2s", "--logs-default-range=1s"},
		{"--shutdown-timeout=-1s"},
		{"--logs-default-limit=0"},
		{"--logs-default-limit=100", "--logs-max-limit=10"},
		{"--logs-max-tail-resume=-1m"},
		{"--request-timeout=-1s"},
		{"--tail-idle-timeout=-1s"},
		{"--auth-cache-ttl=-1s"},
		{"--rate-limit-per-user=-1"},
		{"--rate-limit-per-user=NaN"},
		{"--rate-limit-per-user=+Inf"},
		{"--rate-limit-burst=-1"},
		{"--max-concurrent-per-user=-1"},
		{"--max-tails-per-user=-1"},
		{"--max-tails=-1"},
		{"--metrics-shortterm-store="},
		{"--metrics-longterm-store=Long.Term"},
		{"--metrics-shortterm-retention=0s"},
		{"--metrics-max-range=0s"},
		{"--metrics-max-points=1"},
		{"--metrics-max-timeout=0s"},
		{"--unknown"},
		// The proxy would cut the request while vmselect is still within
		// the timeout it was given.
		{"--request-timeout=10s", "--metrics-max-timeout=30s"},
		{"--request-timeout=30s", "--metrics-max-timeout=30s"},
		{"unexpected", "--root-tenant=tenant-x"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) accepted", args)
		}
	}
}

// The vmselect timeout must stay strictly below a request timeout, and is
// free when the request has none.
func TestParseFlagsTimeoutPairs(t *testing.T) {
	for _, args := range [][]string{
		{"--request-timeout=31s", "--metrics-max-timeout=30s"},
		{"--request-timeout=0", "--metrics-max-timeout=10m"},
	} {
		if _, err := parseFlags(args); err != nil {
			t.Errorf("parseFlags(%v) refused: %v", args, err)
		}
	}
}
