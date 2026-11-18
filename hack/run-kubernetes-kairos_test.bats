#!/usr/bin/env bats
# Unit tests for run_kairos_pool_test in
# hack/e2e-chainsaw/_lib/run-kubernetes-kairos.sh, the Kairos worker pool case
# of the kubernetes-latest suite. A stub kubectl on PATH plays the cluster, so
# the waits that shell out through `timeout sh -ec` reach it too.
#
# What these pin: a failing wait on the pool (its release, MachineDeployment,
# Machines or Node) leaves the pool's disk import, machines, health check and
# events in the log before the run ends, and the pool is held to the identity
# a Machine-linked Node carries.

load test_helper

REPO_ROOT="$(pwd)"

# Called at the top of every test rather than from setup(): the POSIX lane runs
# this file through hack/cozytest.sh, which knows no setup directive.
kairos_setup() {
  stub_dir="$(mktemp -d)"
  export KSTUB_LOG="${stub_dir}/calls"
  export KSTUB_HR=True\|ReconciliationSucceeded\|
  export KSTUB_HISTORY=deployed
  export KSTUB_KIND=KairosConfigTemplate
  export KSTUB_MACHINE_RC=0
  export KSTUB_MD_RC=0
  export KSTUB_NODE_RC=0
  export KSTUB_PROVIDER_ID=kubevirt://kubernetes-t-kairos-abcde
  export KSTUB_CLUSTER_NS=tenant-test
  export KSTUB_HOSTNAME=kairos-host-identity
  export KSTUB_POD_NODE=kairos-node-0
  export KSTUB_POD_RC=0
  : >"${KSTUB_LOG}"
  cat >"${stub_dir}/kubectl" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>"${KSTUB_LOG}"
case "$*" in
  *" apply "* | apply\ *)
    [ -z "${KSTUB_APPLY_FAIL:-}" ] || exit 1
    payload=$(cat)
    printf '%s\n---\n' "$payload" >>"${KSTUB_LOG}.applied"
    if [ "$(printf '%s\n' "$payload" | yq --unwrapScalar '.kind')" = Pod ]; then
      printf '%s\n' "$payload" >"${KSTUB_LOG}.probe"
    fi
    ;;
  *"get kuberneteses"*) [ -z "${KSTUB_K8S_FAIL:-}" ] || exit 1; printf '%s' "${KSTUB_K8S_VERSION-v1.35}" ;;
  *"get hr "*"strategy.name"*) printf 'RetryOnFailure@True' ;;
  *"get hr "*"history"*) printf '%s\n' "${KSTUB_HISTORY}" ;;
  *"get helmreleases"*"jsonpath"*"Ready"*"message"*) printf '%s\n' "${KSTUB_HR_MESSAGE:-}" ;;
  *"get helmreleases"*"jsonpath"*"Ready"*)
    # KSTUB_HR_FIRST is answered once, on the first read, then KSTUB_HR.
    if [ -n "${KSTUB_HR_FIRST:-}" ] && [ ! -e "${KSTUB_LOG}.hr-first" ]; then
      : >"${KSTUB_LOG}.hr-first"; printf '%s' "${KSTUB_HR_FIRST}"
    else
      printf '%s' "${KSTUB_HR}"
    fi
    ;;
  *"wait"*"machines"*) exit "${KSTUB_MACHINE_RC}" ;;
  *"wait machinedeployment"*) exit "${KSTUB_MD_RC}" ;;
  *"wait"*" node "*) exit "${KSTUB_NODE_RC}" ;;
  *"wait"*" pod kairos-probe"*) exit "${KSTUB_POD_RC}" ;;
  *"get machinedeployment"*"configRef.kind"*) printf '%s' "${KSTUB_KIND}" ;;
  *"get machines"*"nodeRef"*) printf '%s' "${KSTUB_NODEREF-kairos-node-0}" ;;
  *"get machines"*"-o name"*) printf 'machine.cluster.x-k8s.io/m0\n' ;;
  *"get svc"*"loadBalancer"*) printf '192.0.2.200' ;;
  *"get secret"*) [ -z "${KSTUB_SECRET_FAIL:-}" ] || exit 1; printf '%s' "$(printf 'clusters:\n- cluster:\n    server: https://x\ncurrent-context: tenant-integration\n' | base64)" ;;
  *"get node"*"providerID"*) printf '%s' "${KSTUB_PROVIDER_ID}" ;;
  *"get node"*"cluster-namespace"*) printf '%s' "${KSTUB_CLUSTER_NS}" ;;
  *"get node"*"hostname"*) printf '%s' "${KSTUB_HOSTNAME}" ;;
  *"get pod kairos-probe"*"nodeName"*) printf '%s' "${KSTUB_POD_NODE}" ;;
  *"get pods -l kubevirt.io=virt-launcher"*"phase="*) ;;
  *"get pods -l kubevirt.io=virt-launcher"*) printf '%s\n' "${KSTUB_LAUNCHERS:-}" ;;
  *"logs virt-launcher-"*) printf 'console of %s\n' "$4" ;;
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

@test "a pool that joins lets the scheduler place a Ready pod on the Kairos node" {
  kairos_setup
  run_kairos_pool_test t
  grep -qxF '  workerClassName: kairos' "${KSTUB_LOG}.applied"
  yq --exit-status '.spec | has("nodeName") == false' "${KSTUB_LOG}.probe"
  [ "$(yq --unwrapScalar '.spec.nodeSelector."kubernetes.io/hostname"' "${KSTUB_LOG}.probe")" = kairos-host-identity ]
  grep --quiet --fixed-strings 'get pod kairos-probe' "${KSTUB_LOG}"
  grep -qF 'wait machines -n tenant-test -l cluster.x-k8s.io/deployment-name=kubernetes-t-kairos' "${KSTUB_LOG}"
  grep --quiet --fixed-strings 'wait --kubeconfig tenantkubeconfig-t-kairos --context tenant-integration --namespace default pod kairos-probe' "${KSTUB_LOG}"
  if grep -qF 'get datavolumes' "${KSTUB_LOG}"; then echo "FAIL: diagnostics ran on a passing pool"; false; fi
  if grep -qF 'kubevirt.io=virt-launcher' "${KSTUB_LOG}"; then echo "FAIL: serial consoles captured on a passing pool"; false; fi
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a parent cluster without a readable version applies no Kairos pool" {
  for failure in read empty; do
    kairos_setup
    if [ "${failure}" = read ]; then export KSTUB_K8S_FAIL=1; else export KSTUB_K8S_VERSION=; fi
    rc=0
    run_kairos_pool_test t >"${workdir}/output" 2>&1 || rc=$?
    [ "${rc}" -ne 0 ]
    [ ! -e "${KSTUB_LOG}.applied" ]
    unset KSTUB_K8S_FAIL KSTUB_K8S_VERSION
    cd "${REPO_ROOT}"
    rm --recursive --force "${stub_dir}" "${workdir}"
  done
}

@test "a Running Kairos machine without a nodeRef fails before reaching the tenant" {
  kairos_setup
  export KSTUB_NODEREF=
  rc=0
  run_kairos_pool_test t >"${workdir}/output" 2>&1 || rc=$?
  [ "${rc}" -ne 0 ]
  grep --quiet --fixed-strings 'has no nodeRef' "${workdir}/output"
  if grep -qF 'get secret' "${KSTUB_LOG}"; then echo "FAIL: read the tenant kubeconfig without a node"; false; fi
  unset KSTUB_NODEREF
  cd "${REPO_ROOT}"
  rm --recursive --force "${stub_dir}" "${workdir}"
}

@test "the Kairos node wait addresses the tenant context" {
  kairos_setup
  rc=0
  run_kairos_pool_test t >"${workdir}/output" 2>&1 || rc=$?
  [ "${rc}" -eq 0 ]
  grep --quiet --fixed-strings -- '--context tenant-integration node kairos-node-0' "${KSTUB_LOG}"
  cd "${REPO_ROOT}"
  rm --recursive --force "${stub_dir}" "${workdir}"
}

@test "a Ready probe placed on another node cannot prove the Kairos worker" {
  kairos_setup
  export KSTUB_POD_NODE=another-node
  rc=0
  run_kairos_pool_test t >"${workdir}/output" 2>&1 || rc=$?
  [ "${rc}" -ne 0 ]
  grep --quiet --fixed-strings 'Kairos probe was scheduled on another-node, want kairos-node-0' "${workdir}/output"
  cd "${REPO_ROOT}"
  rm --recursive --force "${stub_dir}" "${workdir}"
}

@test "an unassigned probe cannot prove Kairos scheduling even if the Ready wait succeeds" {
  kairos_setup
  export KSTUB_POD_NODE=
  rc=0
  run_kairos_pool_test t >"${workdir}/output" 2>&1 || rc=$?
  [ "${rc}" -ne 0 ]
  grep --quiet --fixed-strings 'Kairos probe was not scheduled' "${workdir}/output"
  cd "${REPO_ROOT}"
  rm --recursive --force "${stub_dir}" "${workdir}"
}

@test "a Kairos node without a hostname fails before probe creation" {
  kairos_setup
  export KSTUB_HOSTNAME=
  rc=0
  run_kairos_pool_test t >"${workdir}/output" 2>&1 || rc=$?
  [ "${rc}" -ne 0 ]
  grep --quiet --fixed-strings 'Kairos node kairos-node-0 has no hostname label' "${workdir}/output"
  [ ! -f "${KSTUB_LOG}.probe" ]
  cd "${REPO_ROOT}"
  rm --recursive --force "${stub_dir}" "${workdir}"
}

@test "a probe that does not become Ready cannot prove the Kairos worker" {
  kairos_setup
  export KSTUB_POD_RC=1
  rc=0
  run_kairos_pool_test t 2>/dev/null || rc=$?
  [ "${rc}" -ne 0 ]
  grep --quiet --fixed-strings 'describe pod kairos-probe' "${KSTUB_LOG}"
  cd "${REPO_ROOT}"
  rm --recursive --force "${stub_dir}" "${workdir}"
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

@test "a MachineDeployment that never scales fails with state collected" {
  kairos_setup
  export KSTUB_MD_RC=1
  rc=0
  run_kairos_pool_test t 2>/dev/null || rc=$?
  [ "${rc}" -ne 0 ]
  diagnostics_collected
  if grep -qF 'wait machines' "${KSTUB_LOG}"; then echo "FAIL: the run waited on machines of a MachineDeployment that never scaled"; false; fi
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a node that never turns Ready fails with state collected" {
  kairos_setup
  export KSTUB_NODE_RC=1
  rc=0
  run_kairos_pool_test t 2>/dev/null || rc=$?
  [ "${rc}" -ne 0 ]
  diagnostics_collected
  grep -qF 'describe node kairos-node-0' "${KSTUB_LOG}"
  if grep -qF 'kairos-probe' "${KSTUB_LOG}"; then echo "FAIL: the run went on past a node that never turned Ready"; false; fi
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a stalled release fails at once, with state collected" {
  kairos_setup
  export KSTUB_HR='Unknown|Progressing|True'
  rc=0
  started=$(date +%s)
  run_kairos_pool_test t 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  [ $(( $(date +%s) - started )) -lt 30 ]
  grep -qF 'failed: Unknown|Progressing|True' "${workdir}/err"
  diagnostics_collected
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a release that failed an action it retries is waited on until Ready" {
  kairos_setup
  # helm-controller retries an install that failed on a transient error.
  export KSTUB_HR_FIRST='False|InstallFailed|'
  run_kairos_pool_test t 2>"${workdir}/err"
  grep -qF 'wait machinedeployment' "${KSTUB_LOG}"
  if grep -qF 'failed: False|InstallFailed' "${workdir}/err"; then echo "FAIL: a retried failure ended the wait"; false; fi
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a release that never turns Ready fails at the bound with its last reason" {
  kairos_setup
  KAIROS_RELEASE_WAIT=3
  export KSTUB_HR='False|InstallFailed|'
  export KSTUB_HR_MESSAGE='values don'"'"'t meet the specifications of the schema'
  rc=0
  run_kairos_pool_test t 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -qF 'did not become Ready within 3s; last: False|InstallFailed|' "${workdir}/err"
  grep -qF "values don't meet the specifications of the schema" "${workdir}/err"
  diagnostics_collected
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a release whose history keeps a failed install fails the passing path" {
  kairos_setup
  # The release wait passes once a retry recovered the install.
  export KSTUB_HR_FIRST='False|InstallFailed|'
  export KSTUB_HISTORY='deployed
failed'
  rc=0
  run_kairos_pool_test t >"${workdir}/out" 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -qF 'kairos-probe' "${KSTUB_LOG}"
  grep -qF 'history' "${KSTUB_LOG}"
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a failing pool prints its release history" {
  kairos_setup
  export KSTUB_MACHINE_RC=1
  export KSTUB_HISTORY=failed
  rc=0
  run_kairos_pool_test t >"${workdir}/out" 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -q 'get hr -n tenant-test kubernetes-nodes-t-kairos .*history' "${KSTUB_LOG}"
  grep -qx 'failed' "${workdir}/err"
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a failing pool's report outlives a failed release history and reads only the pool's own console" {
  kairos_setup
  export KSTUB_MACHINE_RC=1
  export KSTUB_HISTORY=failed
  # Name-sorted, the way kubectl lists them: a VM of another test comes first.
  export KSTUB_LAUNCHERS='virt-launcher-a-foreign-vm-x1
virt-launcher-kubernetes-t-kairos-abcde-z2'
  # The step runs the pool test at top level under set -eu, where a guard that
  # fails without its `|| true` would end the report before the console read.
  rc=0
  sh -c 'set -eu; cd "$1"; . hack/e2e-chainsaw/_lib/run-kubernetes.sh; . hack/e2e-chainsaw/_lib/run-kubernetes-kairos.sh; cd "$2"; run_kairos_pool_test t' \
    sh "${REPO_ROOT}" "${workdir}" >"${workdir}/out" 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -qx 'failed' "${workdir}/err"
  grep -qF 'logs virt-launcher-kubernetes-t-kairos-abcde-z2 -c guest-console-log' "${KSTUB_LOG}"
  if grep -qF 'logs virt-launcher-a-foreign-vm-x1' "${KSTUB_LOG}"; then echo "FAIL: read the console of a VM outside the pool"; false; fi
  cd "${REPO_ROOT}"
  rm --recursive --force "${stub_dir}" "${workdir}"
}

@test "a pool that failed before its VM existed reads no other VM's console" {
  kairos_setup
  export KSTUB_MD_RC=1
  # Another pool whose name starts with the Kairos pool's, as well as md0.
  export KSTUB_LAUNCHERS='virt-launcher-kubernetes-t-kairos2-abcde-q1
virt-launcher-kubernetes-t-md0-abcde-x1'
  rc=0
  run_kairos_pool_test t >"${workdir}/out" 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  if grep -qF 'logs virt-launcher-' "${KSTUB_LOG}"; then echo "FAIL: read the console of a VM outside the pool"; false; fi
  grep -qF 'no virt-launcher Pod matching kubernetes-t-kairos' "${COZY_REPORT_DIR}/snapshots/${COZY_SNAPSHOT_NAME:-kubernetes}/tenant-serial-console/setup-error.log"
  cd "${REPO_ROOT}"
  rm --recursive --force "${stub_dir}" "${workdir}"
}

@test "the Kairos pool is applied with logSerialConsole: true" {
  kairos_setup
  run_kairos_pool_test t
  grep -qxF '  logSerialConsole: true' "${KSTUB_LOG}.applied"
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a pool bootstrapped by anything but Kairos fails" {
  kairos_setup
  export KSTUB_KIND=TalosConfigTemplate
  rc=0
  run_kairos_pool_test t 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -qF "bootstraps from a TalosConfigTemplate, want KairosConfigTemplate" "${workdir}/err"
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a Ready node without a KubeVirt providerID fails" {
  kairos_setup
  export KSTUB_PROVIDER_ID=
  rc=0
  run_kairos_pool_test t 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -qF "want kubevirt://<vm>" "${workdir}/err"
  rm -rf "${stub_dir}" "${workdir}"
}

@test "a Ready node without the cluster-namespace annotation fails" {
  kairos_setup
  export KSTUB_CLUSTER_NS=
  rc=0
  run_kairos_pool_test t 2>"${workdir}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -qF "has cluster-namespace annotation '', want tenant-test" "${workdir}/err"
  rm -rf "${stub_dir}" "${workdir}"
}

@test "the tenant kubeconfig step stops at the first command that fails" {
  for knob in KSTUB_APPLY_FAIL KSTUB_SECRET_FAIL; do
    kairos_setup
    export KAIROS_LB=kairos-lb KAIROS_KC="${workdir}/kc"
    export "${knob}=1"
    rc=0
    cozy_kairos_tenant_kubeconfig t >/dev/null 2>&1 || rc=$?
    [ "${rc}" -ne 0 ] || { echo "the step succeeded with ${knob}" >&2; false; }
    if grep -qF -- '--kubeconfig' "${KSTUB_LOG}"; then echo "the step used a kubeconfig after ${knob}" >&2; false; fi
    unset "${knob}"
    rm -rf "${stub_dir}" "${workdir}"
  done
}

