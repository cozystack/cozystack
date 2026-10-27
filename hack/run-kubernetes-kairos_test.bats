#!/usr/bin/env bats
# Unit tests for run_kairos_pool_test in
# hack/e2e-chainsaw/_lib/run-kubernetes-kairos.sh, the Kairos worker pool case
# of the kubernetes-latest suite. A stub kubectl on PATH plays the cluster, so
# the waits that shell out through `timeout sh -ec` reach it too.
#
# What these pin: every failing wait on the management side leaves the pool's
# disk import, machines, health check and events in the log before the run
# ends, and the pool is held to the identity a Machine-linked Node carries.

load test_helper

REPO_ROOT="$(pwd)"

# Called at the top of every test rather than from setup(): the POSIX lane runs
# this file through hack/cozytest.sh, which knows no setup directive.
kairos_setup() {
  stub_dir="$(mktemp -d)"
  export KSTUB_LOG="${stub_dir}/calls"
  export KSTUB_HR=True\|ReconciliationSucceeded\|
  export KSTUB_KIND=KairosConfigTemplate
  export KSTUB_MACHINE_RC=0
  export KSTUB_PROVIDER_ID=kubevirt://kubernetes-t-kairos-abcde
  export KSTUB_CLUSTER_NS=tenant-test
  : >"${KSTUB_LOG}"
  cat >"${stub_dir}/kubectl" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>"${KSTUB_LOG}"
case "$*" in
  *" apply "* | apply\ *) cat >>"${KSTUB_LOG}.applied" ;;
  *"get kuberneteses"*) printf 'v1.35' ;;
  *"get helmreleases"*"jsonpath"*"Ready"*) printf '%s' "${KSTUB_HR}" ;;
  *"wait"*"machines"*) exit "${KSTUB_MACHINE_RC}" ;;
  *"get machinedeployment"*"configRef.kind"*) printf '%s' "${KSTUB_KIND}" ;;
  *"get machines"*"nodeRef"*) printf 'kairos-node-0' ;;
  *"get machines"*"-o name"*) printf 'machine.cluster.x-k8s.io/m0\n' ;;
  *"get svc"*"loadBalancer"*) printf '192.0.2.200' ;;
  *"get secret"*) printf '%s' "$(printf 'clusters:\n- cluster:\n    server: https://x\n' | base64)" ;;
  *"get node"*"providerID"*) printf '%s' "${KSTUB_PROVIDER_ID}" ;;
  *"get node"*"cluster-namespace"*) printf '%s' "${KSTUB_CLUSTER_NS}" ;;
esac
exit 0
STUB
  chmod +x "${stub_dir}/kubectl"
  export PATH="${stub_dir}:${PATH}"
  workdir="$(mktemp -d)"
  export COZY_REPORT_DIR="${workdir}/report"
  # The library sources its siblings by repo-root-relative path.
  cd "${REPO_ROOT}"
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  . hack/e2e-chainsaw/_lib/run-kubernetes-kairos.sh
  cd "${workdir}"
}

diagnostics_collected() {
  for want in datavolumes.cdi.kubevirt.io kubevirtmachines.infrastructure.cluster.x-k8s.io \
      machines.cluster.x-k8s.io machinehealthchecks.cluster.x-k8s.io "get events" "-c guest-console-log" \
      "get pods -l kubevirt.io=virt-launcher"; do
    grep -qF -- "${want}" "${KSTUB_LOG}" || { echo "diagnostics did not read ${want}" >&2; return 1; }
  done
}

@test "a pool that joins passes and runs a pod on the Kairos node" {
  kairos_setup
  run_kairos_pool_test t
  grep -qxF '  nodeGroupClassName: kairos' "${KSTUB_LOG}.applied"
  grep -qxF '  nodeName: kairos-node-0' "${KSTUB_LOG}.applied"
  grep -qF 'wait machines -n tenant-test -l cluster.x-k8s.io/deployment-name=kubernetes-t-kairos' "${KSTUB_LOG}"
  grep -qF 'wait --kubeconfig tenantkubeconfig-t-kairos -n default pod kairos-probe' "${KSTUB_LOG}"
  if grep -qF 'get datavolumes' "${KSTUB_LOG}"; then echo "FAIL: diagnostics ran on a passing pool"; false; fi
  if grep -qF 'kubevirt.io=virt-launcher' "${KSTUB_LOG}"; then echo "FAIL: serial consoles captured on a passing pool"; false; fi
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a machine that never joins fails with the disk import and machine state collected" {
  kairos_setup
  export KSTUB_MACHINE_RC=1
  rc=0
  run_kairos_pool_test t 2>/dev/null || rc=$?
  [ "${rc}" -ne 0 ]
  diagnostics_collected
  if grep -qF 'kairos-probe' "${KSTUB_LOG}"; then echo "FAIL: the run went on past a failed join"; false; fi
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a release the chart refuses fails at once, with state collected" {
  kairos_setup
  export KSTUB_HR='False|InstallFailed|'
  rc=0
  started=$(date +%s)
  run_kairos_pool_test t 2>/dev/null || rc=$?
  [ "${rc}" -ne 0 ]
  # The refusal is final, so the wait ends on it rather than on its 120s bound.
  [ $(( $(date +%s) - started )) -lt 30 ]
  diagnostics_collected
  if grep -qF 'wait machinedeployment' "${KSTUB_LOG}"; then echo "FAIL: the run waited on a pool whose release failed"; false; fi
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a pool bootstrapped by anything but Kairos fails" {
  kairos_setup
  export KSTUB_KIND=TalosConfigTemplate
  rc=0
  run_kairos_pool_test t 2>/dev/null || rc=$?
  [ "${rc}" -ne 0 ]
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a Ready node without a KubeVirt providerID fails" {
  kairos_setup
  export KSTUB_PROVIDER_ID=
  rc=0
  run_kairos_pool_test t 2>/dev/null || rc=$?
  [ "${rc}" -ne 0 ]
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a Ready node without the cluster-namespace annotation fails" {
  kairos_setup
  export KSTUB_CLUSTER_NS=
  rc=0
  run_kairos_pool_test t 2>/dev/null || rc=$?
  [ "${rc}" -ne 0 ]
  rm -rf "${stub_dir}" "${workdir}"
}
