#!/usr/bin/env bats

@test "Create a tenant Kubernetes control plane with previous version" {
  . hack/e2e-apps/run-kubernetes.sh
  run_kubernetes_test 'keys | sort_by(.) | .[-2]' 'test-previous-version' '59992'
}
