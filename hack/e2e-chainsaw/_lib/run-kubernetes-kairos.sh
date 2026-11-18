#!/bin/sh
# A Kairos worker pool on a tenant cluster that run_kubernetes_test brought up
# and proved healthy. The pool names the built-in `kairos` WorkerClass, so
# it boots the platform worker disk for the cluster's minor and joins through
# the Kairos bootstrap provider; the suite asserts what Talos workers get from
# the same path: a Ready Node linked to its Machine, and a pod running on it.
#
# Each wait on the pool itself (its HelmRelease, MachineDeployment, Machines
# and Node) collects the pool's state before the run fails, so a red run tells
# a disk import that never finished (a private registry answers 403 on the
# DataVolume) apart from a machine that booted and never joined. The tenant API
# waits and the probe pod wait fail without it. Nothing here installs a trap:
# the caller is a Chainsaw script step, whose teardown is cozy_cleanup in its
# `finally`.
#
# The step that runs this in the kubernetes-latest suite estimates the worst
# path of the waits below. It is an estimate, not a bound: kubectl_wait_retry
# may re-attempt a wait after a transient apiserver error, and the step's
# ceiling kills the shell, not a child holding its pipe (see .chainsaw.yaml).

KAIROS_POOL_SUFFIX=kairos
# The bound of the pool's release wait, in seconds; the step budget counts it.
KAIROS_RELEASE_WAIT=120

_cozy_kairos_names() {
  _kp_test="$1"
  KAIROS_NODES="${_kp_test}-${KAIROS_POOL_SUFFIX}"
  KAIROS_HR="kubernetes-nodes-${_kp_test}-${KAIROS_POOL_SUFFIX}"
  KAIROS_MD="kubernetes-${_kp_test}-${KAIROS_POOL_SUFFIX}"
  KAIROS_KC="tenantkubeconfig-${_kp_test}-${KAIROS_POOL_SUFFIX}"
  KAIROS_LB="kubernetes-${_kp_test}-${KAIROS_POOL_SUFFIX}-e2e-lb"
}

# The pool's state on the management side, read when a wait on the pool fails.
# Each request is bounded by --request-timeout, and the release history and
# console reads by their own timeouts. A failed read is said rather than
# skipped, because an empty section and a read that never answered look the
# same otherwise.
cozy_report_kairos_pool_failure() {
  _kp_md="$1"
  _kp_sel="cluster.x-k8s.io/deployment-name=${_kp_md}"
  echo "» Kairos pool ${_kp_md} failed; collecting its state" >&2
  for _kp_read in \
    "datavolumes.cdi.kubevirt.io -o wide" \
    "datavolumes.cdi.kubevirt.io -o yaml" \
    "kubevirtmachines.infrastructure.cluster.x-k8s.io -o wide" \
    "machines.cluster.x-k8s.io -l ${_kp_sel} -o yaml" \
    "machinehealthchecks.cluster.x-k8s.io ${_kp_md} -o yaml" \
    "kairosconfigs.bootstrap.cluster.x-k8s.io -o yaml"; do
    echo "::group::kubectl -n tenant-test get ${_kp_read}" >&2
    # shellcheck disable=SC2086 # the read is a word list on purpose
    if ! kubectl -n tenant-test --request-timeout=20s get ${_kp_read} >&2; then
      echo "» the read above did not answer" >&2
    fi
    echo "::endgroup::" >&2
  done
  echo "::group::events in tenant-test, newest last" >&2
  if ! kubectl -n tenant-test --request-timeout=20s get events --sort-by=.lastTimestamp >&2; then
    echo "» the events read did not answer" >&2
  fi
  echo "::endgroup::" >&2
  echo "::group::guest serial console of ${_kp_md}" >&2
  if ! kubectl -n tenant-test --request-timeout=20s logs -l "${_kp_sel}" -c guest-console-log --tail=200 >&2; then
    echo "» no guest console: the VM never started, or its pod is gone" >&2
  fi
  echo "::endgroup::" >&2
  # A Kairos pool release retries a failed install in place, so its history is
  # the only record of an attempt that failed before the one now running.
  cozy_guard_helmrelease tenant-test "${KAIROS_HR}" required >&2 || true
  # The tail above is the end of the boot; the bundle keeps its start, where a
  # first-boot reset that failed in recovery leaves its journal. Only the pool's
  # own VM is read: a pool that failed before its VM existed gets a note, not the
  # console of another VM in the namespace.
  cozy_capture_tenant_serial_console "Kairos pool ${_kp_md} failed" 1 "${_kp_md}-" || true
}

# kubernetes-nodes renders with the Helm wait off, so its release settles as
# soon as the chart applies. As cozy_wait_helmrelease_upgrade does, only a
# Stalled release fails at once: a Ready=False *Failed reason may come from a
# transient error the release retries in place, and a refusal that stays ends
# at the bound with its message.
cozy_wait_kairos_pool_release() {
  _kp_hr="$1"
  _kp_deadline=$(( $(date +%s) + KAIROS_RELEASE_WAIT ))
  while [ "$(date +%s)" -lt "${_kp_deadline}" ]; do
    _kp_state=$(kubectl -n tenant-test --request-timeout=20s get helmreleases.helm.toolkit.fluxcd.io "${_kp_hr}" \
      -o 'jsonpath={.status.conditions[?(@.type=="Ready")].status}{"|"}{.status.conditions[?(@.type=="Ready")].reason}{"|"}{.status.conditions[?(@.type=="Stalled")].status}' \
      2>/dev/null) || _kp_state=
    case "${_kp_state}" in
      True\|*) return 0 ;;
      *\|True)
        echo "HelmRelease tenant-test/${_kp_hr} failed: ${_kp_state}" >&2
        kubectl -n tenant-test --request-timeout=20s get helmreleases.helm.toolkit.fluxcd.io "${_kp_hr}" \
          -o 'jsonpath={.status.conditions[?(@.type=="Ready")].message}{"\n"}' >&2 || true
        return 1
        ;;
    esac
    sleep 2
  done
  # The last reason is the cause when the chart keeps refusing the release.
  echo "HelmRelease tenant-test/${_kp_hr} did not become Ready within ${KAIROS_RELEASE_WAIT}s; last: ${_kp_state:-unread}" >&2
  kubectl -n tenant-test --request-timeout=20s get helmreleases.helm.toolkit.fluxcd.io "${_kp_hr}" \
    -o 'jsonpath={.status.conditions[?(@.type=="Ready")].message}{"\n"}' >&2 || true
  return 1
}

# The same test-scoped LoadBalancer run_kubernetes_test reaches the tenant
# through, under a name of its own: that run deletes its LB and kubeconfig on
# success. The label lets cozy_cleanup reap this one with the others.
cozy_kairos_tenant_kubeconfig() {
  _kp_test="$1"
  # Callers test this function in a condition, which turns errexit off here.
  kubectl -n tenant-test delete service "${KAIROS_LB}" --ignore-not-found --wait=true --timeout=60s || return 1
  kubectl --request-timeout=20s apply -n tenant-test -f - <<EOF || return 1
apiVersion: v1
kind: Service
metadata:
  name: ${KAIROS_LB}
  labels:
    cozystack-e2e.io/tenant-api-lb: "${_kp_test}"
spec:
  type: LoadBalancer
  selector:
    kamaji.clastix.io/name: kubernetes-${_kp_test}
  ports:
  - name: kube-apiserver
    port: 6443
    targetPort: 6443
EOF
  timeout 90 sh -ec "until [ -n \"\$(kubectl get svc -n tenant-test \"\$1\" -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null)\" ]; do sleep 2; done" sh "${KAIROS_LB}" || return 1
  _kp_ip=$(kubectl --request-timeout=20s get svc -n tenant-test "${KAIROS_LB}" -o jsonpath='{.status.loadBalancer.ingress[0].ip}') || return 1
  _kp_conf=$(kubectl --request-timeout=20s get secret "kubernetes-${_kp_test}-admin-kubeconfig" -n tenant-test -ojsonpath='{.data.super-admin\.conf}') || return 1
  [ -n "${_kp_conf}" ] || { echo "kubernetes-${_kp_test}-admin-kubeconfig holds no super-admin.conf" >&2; return 1; }
  printf '%s' "${_kp_conf}" | base64 -d > "${KAIROS_KC}" || return 1
  # Same trade as run_kubernetes_test: the LB IP is not in the serving
  # certificate, and the suite tests the cluster, not its serving identity.
  yq -i ".clusters[0].cluster.server = \"https://${_kp_ip}:6443\" | .clusters[0].cluster.\"insecure-skip-tls-verify\" = true | del(.clusters[0].cluster.\"certificate-authority-data\")" "${KAIROS_KC}" || return 1
  timeout 60 sh -ec 'until kubectl --kubeconfig '"${KAIROS_KC}"' get --raw /healthz >/dev/null 2>&1; do sleep 2; done' || return 1
}

# A Node linked to its Machine carries both of these. kubevirt-csi-node refuses
# to start on a Node without them, so a missing one is a broken worker even
# when the Node reports Ready.
cozy_check_kairos_node_identity() {
  _kp_node="$1"
  _kp_pid=$(kubectl --kubeconfig "${KAIROS_KC}" --request-timeout=20s get node "${_kp_node}" -o 'jsonpath={.spec.providerID}')
  _kp_ns=$(kubectl --kubeconfig "${KAIROS_KC}" --request-timeout=20s get node "${_kp_node}" -o 'jsonpath={.metadata.annotations.cluster\.x-k8s\.io/cluster-namespace}')
  case "${_kp_pid}" in
    kubevirt://?*) ;;
    *) echo "Kairos node ${_kp_node} has providerID '${_kp_pid}', want kubevirt://<vm>" >&2; return 1 ;;
  esac
  if [ "${_kp_ns}" != tenant-test ]; then
    echo "Kairos node ${_kp_node} has cluster-namespace annotation '${_kp_ns}', want tenant-test" >&2
    return 1
  fi
}

run_kairos_pool_test() {
  _kp_test="$1"
  _cozy_kairos_names "${_kp_test}"
  _kp_sc=$(cozy_e2e_storage_class) || return 1
  _kp_version=$(kubectl -n tenant-test --request-timeout=20s get kuberneteses.apps.cozystack.io "${_kp_test}" -o 'jsonpath={.spec.version}') || return 1
  [ -n "${_kp_version}" ] || { echo "Kubernetes ${_kp_test} has no spec.version for its Kairos pool" >&2; return 1; }

  kubectl --request-timeout=20s apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: KubernetesNodes
metadata:
  name: "${KAIROS_NODES}"
  namespace: tenant-test
spec:
  cluster: "${_kp_test}"
  workerClassName: kairos
  diskSize: 20Gi
  gpus: []
  instanceType: u1.medium
  logSerialConsole: true
  minReplicas: 1
  maxReplicas: 1
  podCpuLimit: 2
  podCpuRequest: 100m
  resources:
    cpu: 1
    memory: 4Gi
  roles: []
  storageClass: "${_kp_sc}"
  version: "${_kp_version}"
EOF

  # Existence backstop before each wait: the child release, and then the
  # MachineDeployment it renders, are created asynchronously and `kubectl wait`
  # on an absent object exits NotFound instead of polling.
  if ! timeout 1m sh -ec 'until kubectl -n tenant-test get helmreleases.helm.toolkit.fluxcd.io '"${KAIROS_HR}"' >/dev/null 2>&1; do sleep 2; done' \
    || ! cozy_wait_kairos_pool_release "${KAIROS_HR}"; then
    cozy_report_kairos_pool_failure "${KAIROS_MD}"
    return 1
  fi
  if ! timeout 1m sh -ec 'until kubectl -n tenant-test get machinedeployment '"${KAIROS_MD}"' >/dev/null 2>&1; do sleep 2; done' \
    || ! kubectl_wait_retry machinedeployment "${KAIROS_MD}" -n tenant-test --timeout=2m --for=jsonpath='{.status.replicas}'=1; then
    cozy_report_kairos_pool_failure "${KAIROS_MD}"
    return 1
  fi
  _kp_kind=$(kubectl -n tenant-test --request-timeout=20s get machinedeployment "${KAIROS_MD}" -o 'jsonpath={.spec.template.spec.bootstrap.configRef.kind}')
  if [ "${_kp_kind}" != KairosConfigTemplate ]; then
    echo "MachineDeployment ${KAIROS_MD} bootstraps from a ${_kp_kind}, want KairosConfigTemplate" >&2
    return 1
  fi

  # Running means the Machine has a nodeRef: the disk imported, the guest
  # booted, reset into the installed system, rebooted and joined. No
  # remediation has to fit in this window: at the default maxUnhealthy the
  # MachineHealthCheck never replaces the only machine of a pool.
  if ! timeout 1m sh -ec "until [ -n \"\$(kubectl -n tenant-test get machines -l \"cluster.x-k8s.io/deployment-name=\$1\" -o name 2>/dev/null)\" ]; do sleep 2; done" sh "${KAIROS_MD}" \
    || ! kubectl_wait_retry machines -n tenant-test -l "cluster.x-k8s.io/deployment-name=${KAIROS_MD}" --timeout=15m --for=jsonpath='{.status.phase}'=Running; then
    cozy_report_kairos_pool_failure "${KAIROS_MD}"
    return 1
  fi
  _kp_node=$(kubectl -n tenant-test --request-timeout=20s get machines -l "cluster.x-k8s.io/deployment-name=${KAIROS_MD}" -o 'jsonpath={.items[0].status.nodeRef.name}') || return 1
  [ -n "${_kp_node}" ] || { echo "the Running Kairos machine of ${KAIROS_MD} has no nodeRef" >&2; return 1; }

  if ! cozy_kairos_tenant_kubeconfig "${_kp_test}"; then
    echo "could not reach the tenant API to check the Kairos node" >&2
    return 1
  fi
  _kp_context=$(yq --unwrapScalar '."current-context"' "${KAIROS_KC}") || return 1
  case "${_kp_context}" in ''|null) echo "Kairos tenant kubeconfig has no context" >&2; return 1 ;; esac
  if ! kubectl_wait_retry --kubeconfig "${KAIROS_KC}" --context "${_kp_context}" node "${_kp_node}" --timeout=3m --for=condition=Ready; then
    kubectl --context "${_kp_context}" --kubeconfig "${KAIROS_KC}" --request-timeout=20s describe node "${_kp_node}" >&2 || true
    cozy_report_kairos_pool_failure "${KAIROS_MD}"
    return 1
  fi
  cozy_check_kairos_node_identity "${_kp_node}" || return 1
  _kp_hostname=$(kubectl --context "${_kp_context}" --kubeconfig "${KAIROS_KC}" --request-timeout=20s get node "${_kp_node}" --output 'jsonpath={.metadata.labels.kubernetes\.io/hostname}') || return 1
  case "${_kp_hostname}" in ''|null) echo "Kairos node ${_kp_node} has no hostname label" >&2; return 1 ;; esac

  kubectl --context "${_kp_context}" --kubeconfig "${KAIROS_KC}" --request-timeout=20s --namespace default apply --filename - <<EOF || return 1
apiVersion: v1
kind: Pod
metadata:
  name: kairos-probe
  namespace: default
spec:
  nodeSelector:
    kubernetes.io/hostname: "${_kp_hostname}"
  containers:
  - name: nginx
    image: nginx:1.31.3-alpine@sha256:4a73073bd557c65b759505da037898b61f1be6cbcc3c2c3aeac22d2a470c1752
    readinessProbe:
      httpGet:
        path: /
        port: 80
      periodSeconds: 2
EOF
  if ! kubectl_wait_retry --kubeconfig "${KAIROS_KC}" --context "${_kp_context}" --namespace default pod kairos-probe --timeout=2m --for=condition=Ready; then
    kubectl --context "${_kp_context}" --kubeconfig "${KAIROS_KC}" --request-timeout=20s --namespace default describe pod kairos-probe >&2 || true
    return 1
  fi
  _kp_scheduled=$(kubectl --context "${_kp_context}" --kubeconfig "${KAIROS_KC}" --request-timeout=20s --namespace default get pod kairos-probe --output 'jsonpath={.spec.nodeName}') || return 1
  [ -n "${_kp_scheduled}" ] || { echo "Kairos probe was not scheduled" >&2; return 1; }
  [ "${_kp_scheduled}" = "${_kp_node}" ] || { echo "Kairos probe was scheduled on ${_kp_scheduled}, want ${_kp_node}" >&2; return 1; }
  echo "» Kairos pool ${KAIROS_MD}: node ${_kp_node} Ready and running a scheduler-placed pod"
  # The release wait passes a failed install that a retry recovered, and the
  # Snapshot it leaves is the only record of it; e2e-testing.md §6.
  cozy_guard_helmrelease tenant-test "${KAIROS_HR}" required || return 1

  kubectl -n tenant-test --request-timeout=20s delete service "${KAIROS_LB}" --ignore-not-found --wait=false
  rm -f "${KAIROS_KC}"
}
