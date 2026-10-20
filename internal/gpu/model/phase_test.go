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

package model

import (
	"testing"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

func TestPhaseTable(t *testing.T) {
	tests := []struct {
		phase                  gpuv1alpha1.GPUClaimPhase
		holds, counts, records bool
	}{
		{phase: "", holds: false, counts: false, records: false},
		{phase: gpuv1alpha1.GPUClaimPending, holds: false, counts: false, records: false},
		{phase: gpuv1alpha1.GPUClaimBound, holds: true, counts: true, records: true},
		{phase: gpuv1alpha1.GPUClaimAttached, holds: true, counts: true, records: true},
		{phase: gpuv1alpha1.GPUClaimLost, holds: false, counts: true, records: false},
	}
	for _, tc := range tests {
		t.Run(string(tc.phase), func(t *testing.T) {
			if got := HoldsGPUs(tc.phase); got != tc.holds {
				t.Errorf("HoldsGPUs = %v, want %v", got, tc.holds)
			}
			if got := CountsAgainstBudget(tc.phase); got != tc.counts {
				t.Errorf("CountsAgainstBudget = %v, want %v", got, tc.counts)
			}
			if got := InUsageRecords(tc.phase); got != tc.records {
				t.Errorf("InUsageRecords = %v, want %v", got, tc.records)
			}
		})
	}
}
