package main

import "testing"

func TestRequiresExplicitTargetAndRenderProofInputs(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("implicit kubeconfig/target allowed")
	}
}
