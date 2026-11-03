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
}

func TestParseFlagsOverrides(t *testing.T) {
	o, err := parseFlags([]string{"--cluster-domain=example.org", "--logs-max-range=24h", "--max-tails=3"})
	if err != nil {
		t.Fatal(err)
	}
	if o.clusterDomain != "example.org" || o.logs.MaxRange != 24*time.Hour || o.limiter.MaxTails != 3 {
		t.Errorf("overrides not applied: %+v", o)
	}
}

func TestParseFlagsInvalid(t *testing.T) {
	for _, args := range [][]string{
		{"--root-tenant="},
		{"--cluster-domain="},
		{"--logs-default-range=48h", "--logs-max-range=24h"},
		{"--logs-max-buckets=2"},
		{"--logs-max-range=2s", "--logs-default-range=1s"},
		{"--shutdown-timeout=-1s"},
		{"--logs-default-limit=0"},
		{"--logs-default-limit=100", "--logs-max-limit=10"},
		{"--logs-max-tail-resume=-1m"},
		{"--request-timeout=-1s"},
		{"--tail-idle-timeout=-1s"},
		{"--auth-cache-ttl=-1s"},
		{"--rate-limit-per-user=-1"},
		{"--rate-limit-burst=-1"},
		{"--max-concurrent-per-user=-1"},
		{"--max-tails-per-user=-1"},
		{"--max-tails=-1"},
		{"--unknown"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) accepted", args)
		}
	}
}
