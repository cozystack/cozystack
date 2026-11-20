#!/usr/bin/env bash
set -euo pipefail
. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
export KUBECONFIG=synthetic
cozy_worker_upgrade_budget() { :; }
cozy_worker_upgrade_reserve_from() { echo 0; }
yq() { echo synthetic; }
cozy_worker_upgrade_tenant() { echo connect; }
cozy_worker_upgrade_default_create() { echo create-one; }
cozy_worker_upgrade_default_node() { echo node-identity; }
cozy_worker_upgrade_default_probe() { echo scheduled-pod; }
cozy_worker_upgrade_default_drain() { echo scale-zero-and-drain; }
cozy_worker_upgrade_default_test example
