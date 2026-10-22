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
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cozystack/cozystack/internal/gpu"
)

func stepNames(steps []step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.name)
	}
	return out
}

// The steps are built with a nil manager and never run: building the list
// must not touch the manager, or this test would not be able to list them.
func TestGPUOnlySelectsOnlyTheGPUSteps(t *testing.T) {
	steps := managerSteps(nil, setupConfig{gpu: gpu.Config{Enabled: true}})
	got := stepNames(selectSteps(steps, true))
	want := []string{"gpu-binder", "gpu-webhooks", "healthz", "readyz"}
	if !slices.Equal(got, want) {
		t.Fatalf("--gpu-only runs %v, want exactly %v", got, want)
	}
}

func TestDefaultRunsEveryStep(t *testing.T) {
	steps := managerSteps(nil, setupConfig{gpu: gpu.Config{Enabled: true}})
	got := stepNames(selectSteps(steps, false))
	want := []string{
		"WorkloadMonitor", "WorkloadReconciler", "ApplicationDefinitionReconciler",
		"ApplicationDefinitionHelmReconciler", "TenantGateway", "TenantQuota", "TenantLogRouting",
		"WildcardSecret", "CACert", "gpu-binder", "gpu-webhooks", "healthz", "readyz", "telemetry",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("default mode runs %v, want %v", got, want)
	}
}

func TestValidateGPUOnly(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		gpuOnly, enabled, leaderElect bool
		leaseID                       string
		wantErr                       bool
	}{
		{name: "gpu-only without enable-gpu", gpuOnly: true, leaseID: defaultLeaderElectionID, wantErr: true},
		{name: "gpu-only electing on the main controller's Lease", gpuOnly: true, enabled: true, leaderElect: true,
			leaseID: defaultLeaderElectionID, wantErr: true},
		{name: "gpu-only electing on its own Lease", gpuOnly: true, enabled: true, leaderElect: true, leaseID: "gpu-only.example"},
		{name: "gpu-only without election", gpuOnly: true, enabled: true, leaseID: defaultLeaderElectionID},
		{name: "default mode on the default Lease", leaderElect: true, leaseID: defaultLeaderElectionID},
		{name: "default mode with GPU", enabled: true, leaderElect: true, leaseID: defaultLeaderElectionID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGPUOnly(tc.gpuOnly, tc.enabled, tc.leaderElect, tc.leaseID)
			if (err != nil) != tc.wantErr {
				t.Errorf("got %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

// A flag refusal exits before the manager starts, so it has to be logged by
// then: before the logger is set the message goes nowhere and the pod
// crash-loops without saying why.
func TestFlagRefusalIsLogged(t *testing.T) {
	if os.Getenv("TEST_GPU_ONLY_REFUSAL_HELPER") == "1" {
		os.Args = []string{"cozystack-controller", "--gpu-only"}
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFlagRefusalIsLogged$")
	// Should the refusal ever stop exiting, main() would go on to start a
	// manager: give it no cluster to reach, neither a kubeconfig nor the
	// in-cluster service account.
	cmd.Env = append(os.Environ(), "TEST_GPU_ONLY_REFUSAL_HELPER=1", "KUBECONFIG=/nonexistent", "KUBERNETES_SERVICE_HOST=")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want exit code 1, got %v; output:\n%s", err, out)
	}
	if !strings.Contains(string(out), "--gpu-only needs --enable-gpu") {
		t.Fatalf("the refusal is not in the output:\n%s", out)
	}
}
