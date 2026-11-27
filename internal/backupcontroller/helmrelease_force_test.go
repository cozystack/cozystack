// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A request helm-controller already handled must not stop a new one, or a
// release forced once before the restore would never be upgraded after it.
func TestHelmForcePending(t *testing.T) {
	const stamp = "2026-01-01T00:00:00Z"
	tests := []struct {
		name    string
		ann     map[string]string
		handled string
		want    bool
	}{
		{name: "no request"},
		{name: "reconcile request only", ann: map[string]string{"reconcile.fluxcd.io/requestedAt": stamp}},
		{name: "stamps differ", ann: map[string]string{"reconcile.fluxcd.io/forceAt": stamp, "reconcile.fluxcd.io/requestedAt": "other"}},
		{name: "unhandled", ann: map[string]string{"reconcile.fluxcd.io/forceAt": stamp, "reconcile.fluxcd.io/requestedAt": stamp}, want: true},
		{name: "handled", ann: map[string]string{"reconcile.fluxcd.io/forceAt": stamp, "reconcile.fluxcd.io/requestedAt": stamp}, handled: stamp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hr := makeUnstructuredHelmRelease("vm-disk-data", "tenant-root", nil)
			hr.SetAnnotations(tt.ann)
			if tt.handled != "" {
				if err := unstructured.SetNestedField(hr.Object, tt.handled, "status", "lastHandledForceAt"); err != nil {
					t.Fatal(err)
				}
			}
			if got := helmForcePending(hr); got != tt.want {
				t.Errorf("helmForcePending = %v, want %v", got, tt.want)
			}
		})
	}
}
