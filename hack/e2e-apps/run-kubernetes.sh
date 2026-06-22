. hack/e2e-apps/remediation-guard.sh

# Unconditional cleanup hook, invoked by cozytest.sh after this file's tests
# (pass or fail). A failed run otherwise leaves the tenant cluster's worker-VM
# PVCs (tens of GiB) in tenant-test, exhausting the shared tenant-quota and
# cascade-failing every storage-heavy app that runs afterwards. Delete the
# cluster(s) and wait for teardown so the quota is freed. Defined at file scope
# (not inside the @test) so cozytest.sh's parent-shell EXIT trap can reach it.
cozy_cleanup() {
  # Delete any test-scoped tenant API LoadBalancer Services left by a failed run
  # so they don't leak MetalLB IPs from the shared host pool. Labeled by the
  # test so a single selector reaps them all.
  kubectl -n tenant-test delete service -l cozystack-e2e.io/tenant-api-lb --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io --all --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kuberneteses.apps.cozystack.io --all --for=delete --timeout=5m 2>/dev/null || true
}

# Snapshot the tenant cluster (its cilium/CSI/coredns internals) on a failed run.
# Registered as an EXIT trap INSIDE run_kubernetes_test so it fires during THIS
# test subshell's exit, before the success path (or cozy_cleanup) deletes the
# tenant API LoadBalancer. crust-gather reaches the tenant only through the
# kubeconfig's server URL (it connects directly — no host-proxy mode — and the
# in-cluster URL is unreachable from the runner), which is the LB IP and stays
# routable until teardown. CURRENT_TENANT_KC is a global so the handler can read
# it regardless of function scope at EXIT-trap time.
_tenant_snapshot_on_fail() {
  _rc=$?
  [ "$_rc" -eq 0 ] && return 0
  command -v crust-gather >/dev/null 2>&1 || return 0
  [ -n "${CURRENT_TENANT_KC:-}" ] && [ -f "${CURRENT_TENANT_KC}" ] || return 0
  _snap="${COZY_REPORT_DIR:-_out/cozyreport}/snapshots/$(basename "${TEST_FILE:-tenant}" .bats)"
  mkdir -p "$_snap" 2>/dev/null || true
  echo "» capturing tenant crust-gather snapshot (${CURRENT_TENANT_KC}) before teardown"
  # Bounded with a timeout for the same reason as the host snapshot in
  # cozytest.sh: an unbounded collect can hang for hours and wedge the job.
  # (timeout's own -k 30 / 300 are distinct from crust-gather's -k kubeconfig.)
  timeout -k 30 300 crust-gather collect -k "${CURRENT_TENANT_KC}" --exclude-kind Secret -f "$_snap/${CURRENT_TENANT_KC}" >/dev/null 2>&1 || true
}

run_kubernetes_test() {
    local version_expr="$1"
    local test_name="$2"
    local port="$3"
    # Optional: when "true", enable the ouroboros addon on the Kubernetes CR
    # and run the hairpin-NAT reconciliation assertions after the cluster is
    # Ready. Folded in here so we don't pay a second ~25m Kamaji bringup just
    # to flip one addon flag — kubernetes-latest passes "true", kubernetes-
    # previous leaves it empty.
    local enable_ouroboros="${4:-}"
    local k8s_version
    k8s_version=$(yq "$version_expr" packages/apps/kubernetes/files/versions.yaml)

  # Clean up stale resources from a previous failed retry
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io "${test_name}" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kuberneteses.apps.cozystack.io "${test_name}" --for=delete --timeout=2m 2>/dev/null || true

  # Compose the optional ouroboros addon block. Indentation matches the
  # surrounding addons map (4 spaces).
  local ouroboros_addon=""
  if [ "${enable_ouroboros}" = "true" ]; then
    ouroboros_addon=$(cat <<'YAML'
    ouroboros:
      enabled: true
      # logLevel=debug surfaces controller informer events for failure
      # diagnosis; scoped to the e2e fixture only, production tenants stay
      # on the upstream chart default (info).
      valuesOverride:
        ouroboros:
          controller:
            logLevel: debug
YAML
)
  fi

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: "${test_name}"
  namespace: tenant-test
spec:
  addons:
    certManager:
      enabled: false
      valuesOverride: {}
    cilium:
      valuesOverride: {}
    fluxcd:
      enabled: false
      valuesOverride: {}
    gatewayAPI:
      enabled: false
    gpuOperator:
      enabled: false
      valuesOverride: {}
    ingressNginx:
      enabled: true
      hosts: []
      valuesOverride: {}
    monitoringAgents:
      enabled: false
      valuesOverride: {}
${ouroboros_addon}
    verticalPodAutoscaler:
      valuesOverride: {}
  controlPlane:
    apiServer:
      resources: {}
      resourcesPreset: small
    controllerManager:
      resources: {}
      resourcesPreset: micro
    konnectivity:
      server:
        resources: {}
        resourcesPreset: micro
    replicas: 2
    scheduler:
      resources: {}
      resourcesPreset: micro
  host: ""
  nodeGroups:
    md0:
      diskSize: 20Gi
      gpus: []
      instanceType: u1.medium
      maxReplicas: 10
      minReplicas: 2
      resources: {}
      roles:
      - ingress-nginx
  storageClass: replicated
  version: "${k8s_version}"
EOF
  # Wait for the tenant-test namespace to be active
  kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active

  # Wait for the Kamaji control plane to be created. Under Flux v2.8
  # kstatus-based health checks helm-controller can take 20-30s to dispatch
  # the new Kubernetes HR before it renders the KamajiControlPlane CR; the
  # old 10s budget was tight on v2.7 and consistently fails on v2.8.
  timeout 2m sh -ec 'until kubectl get kamajicontrolplane -n tenant-test kubernetes-'"${test_name}"'; do sleep 1; done'

  # Wait for the tenant control plane to be fully created (timeout after 4 minutes)
  kubectl wait --for=condition=TenantControlPlaneCreated kamajicontrolplane -n tenant-test kubernetes-${test_name} --timeout=4m

  # Wait for Kubernetes resources to be ready (timeout after 2 minutes)
  kubectl wait tcp -n tenant-test kubernetes-${test_name} --timeout=5m --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready

  # Wait for all required deployments to be available (timeout after 4 minutes)
  kubectl wait deploy --timeout=4m --for=condition=available -n tenant-test kubernetes-${test_name} kubernetes-${test_name}-cluster-autoscaler kubernetes-${test_name}-kccm kubernetes-${test_name}-kcsi-controller

  # Wait for the machine deployment to scale to 2 replicas (timeout after 1 minute)
  kubectl wait machinedeployment kubernetes-${test_name}-md0 -n tenant-test --timeout=1m --for=jsonpath='{.status.replicas}'=2
  # Get the admin kubeconfig and save it to a file
  kubectl get secret kubernetes-${test_name}-admin-kubeconfig -ojsonpath='{.data.super-admin\.conf}' -n tenant-test | base64 -d > "tenantkubeconfig-${test_name}"

  # Expose the tenant Kubernetes API via a test-scoped LoadBalancer instead of
  # `kubectl port-forward`. The host cluster runs MetalLB on the same /24 as the
  # sandbox nodes (pool 192.168.123.200-250), so an LB IP is directly routable
  # from the test — the in-tenant LB test below already curls such an address.
  # Crucially, a LoadBalancer Service load-balances across ALL ready apiserver
  # endpoints (both Kamaji control-plane pods), so a single apiserver pod restart
  # is routed around transparently. `kubectl port-forward` instead pins to one
  # pod and dies when that pod blips: a lone kube-apiserver restart was observed
  # leaving localhost refusing connections for the entire 12m node-Ready wait
  # while the cluster was in fact healthy (CAPI NodeHealthy=True on both nodes),
  # failing the test on a dead tunnel. The LB endpoint is also stable until
  # teardown, so the failure snapshot can still reach the tenant. Test-scoped and
  # additive — no change to the product Kamaji/Kubernetes chart.
  #
  # Clean up a stale LB from a previous failed retry of this same test first.
  kubectl -n tenant-test delete service "kubernetes-${test_name}-e2e-lb" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl apply -n tenant-test -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: kubernetes-${test_name}-e2e-lb
  labels:
    cozystack-e2e.io/tenant-api-lb: "${test_name}"
spec:
  type: LoadBalancer
  selector:
    kamaji.clastix.io/name: kubernetes-${test_name}
  ports:
  - name: kube-apiserver
    port: 6443
    targetPort: 6443
EOF
  # Wait for MetalLB to assign an external IP.
  timeout 90 sh -ec 'until [ -n "$(kubectl get svc -n tenant-test kubernetes-'"${test_name}"'-e2e-lb -o jsonpath="{.status.loadBalancer.ingress[0].ip}" 2>/dev/null)" ]; do sleep 2; done'
  TENANT_API_LB_IP=$(kubectl get svc -n tenant-test "kubernetes-${test_name}-e2e-lb" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
  if [ -z "${TENANT_API_LB_IP}" ]; then
    echo "tenant API LoadBalancer did not receive an IP" >&2
    exit 1
  fi

  # Point the kubeconfig at the LB IP. The MetalLB IP is not in the apiserver
  # serving-cert SANs, so skip TLS verification (e2e only — we functionally test
  # the cluster, not its serving identity) and drop the now-mismatched CA data
  # (kubectl rejects insecure-skip-tls-verify alongside certificate-authority).
  yq -i ".clusters[0].cluster.server = \"https://${TENANT_API_LB_IP}:6443\" | .clusters[0].cluster.\"insecure-skip-tls-verify\" = true | del(.clusters[0].cluster.\"certificate-authority-data\")" "tenantkubeconfig-${test_name}"

  # Wait for the API to answer through the LB before using it.
  timeout 60 sh -ec 'until kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get --raw /healthz >/dev/null 2>&1; do sleep 2; done'
  # The kubeconfig + LB are live now. Arm the tenant snapshot: any failure from
  # here on captures the tenant cluster (the LB endpoint stays up until teardown,
  # so crust-gather can reach it). Cleared on the success path below.
  CURRENT_TENANT_KC="tenantkubeconfig-${test_name}"
  trap '_tenant_snapshot_on_fail' EXIT
  # Verify the Kubernetes version matches what we expect (retry for up to 20 seconds)
  timeout 20 sh -ec 'until kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' version 2>/dev/null | grep -Fq "Server Version: ${k8s_version}"; do sleep 1; done'

  # Wait until at least 2 worker nodes have joined AND become Ready, on a single
  # deadline. This used to be split (8m to join + 3m to become Ready), but the
  # two budgets starve each other under load: a slow KubeVirt VM boot consumes
  # the join budget, then the tenant cluster's cilium CNI needs several more
  # minutes to make the freshly-joined nodes Ready — overflowing the fixed 3m
  # Ready window even though the CNI converges fine. One 12m deadline that polls
  # for ">=2 nodes Ready" is robust to wherever the time goes.
  if ! timeout 12m bash -c '
    until [ "$(kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get nodes --no-headers 2>/dev/null | grep -cw Ready)" -ge 2 ]; do
      sleep 5
    done
  '; then
    # Dump debug info and fail fast — no point running LB/NFS tests without Ready nodes
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" describe nodes
    kubectl -n tenant-test get hr
    exit 1
  fi
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" get nodes -o wide

  # Verify the kubelet version matches what we expect
  versions=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" \
    get nodes -o jsonpath='{.items[*].status.nodeInfo.kubeletVersion}')

  node_ok=true

  for v in $versions; do
    case "$v" in
      "${k8s_version}" | "${k8s_version}".* | "${k8s_version}"-*)
        # acceptable
        ;;
      *)
        node_ok=false
        break
        ;;
    esac
  done

  if [ "$node_ok" != true ]; then
    echo "Kubelet versions did not match expected ${k8s_version}" >&2
    exit 1
  fi


  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: tenant-test
EOF

  # Clean up backend resources from any previous failed attempt
  kubectl delete deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
    -n tenant-test --ignore-not-found --timeout=60s || true
  kubectl delete service --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
    -n tenant-test --ignore-not-found --timeout=60s || true

  # Backend 1
  kubectl apply --kubeconfig "tenantkubeconfig-${test_name}" -f- <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: "${test_name}-backend"
  namespace: tenant-test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: backend
      backend: "${test_name}-backend"
  template:
    metadata:
      labels:
        app: backend
        backend: "${test_name}-backend"
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        ports:
        - containerPort: 80
        readinessProbe:
          httpGet:
            path: /
            port: 80
          initialDelaySeconds: 2
          periodSeconds: 2
EOF

  # LoadBalancer Service
  kubectl apply --kubeconfig "tenantkubeconfig-${test_name}" -f- <<EOF
apiVersion: v1
kind: Service
metadata:
  name: "${test_name}-backend"
  namespace: tenant-test
spec:
  type: LoadBalancer
  selector:
    app: backend
    backend: "${test_name}-backend"
  ports:
  - port: 80
    targetPort: 80
EOF

  # Wait for pods readiness
  kubectl wait deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test --for=condition=Available --timeout=300s

  # Wait for LoadBalancer to be provisioned (IP or hostname)
  timeout 90 sh -ec "
    until kubectl get svc ${test_name}-backend --kubeconfig tenantkubeconfig-${test_name} -n tenant-test \
      -o jsonpath='{.status.loadBalancer.ingress[0]}' | grep -q .; do
      sleep 5
    done
  "

  LB_ADDR=$(
    kubectl get svc --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
      -n tenant-test \
      -o jsonpath='{.status.loadBalancer.ingress[0].ip}{.status.loadBalancer.ingress[0].hostname}'
  )

  if [ -z "$LB_ADDR" ]; then
    echo "LoadBalancer address is empty" >&2
    exit 1
  fi

  # TODO(e2e-replace-fixed-timeouts): genuine retry loop. This validates an
  # external HTTP path (MetalLB-advertised LB IP -> in-tenant ingress ->
  # backend pod) which is not visible to the Kubernetes API as a single
  # condition, so kubectl wait cannot replace it. The 20x3s = 60s budget is
  # capped with `lb_ok=false` then asserted below.
  lb_ok=false
  for i in $(seq 1 20); do
    echo "Attempt $i"
    if curl --silent --fail "http://${LB_ADDR}"; then
      lb_ok=true
      break
    fi
    sleep 3
  done

  if [ "$lb_ok" != true ]; then
    echo "LoadBalancer not reachable" >&2
    exit 1
  fi

  # Cleanup
  kubectl delete deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test
  kubectl delete service --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test

  # Block until csi.kubevirt.io is registered on the tenant worker CSINode.
  # Otherwise the NFS pod schedules while kubevirt-csi-node DaemonSet is
  # still rolling out, eats ~1m on FailedAttachVolume retries, and trips
  # the 5m pod-Succeeded budget when containerd's CreateContainer stalls.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}-csi" --timeout=10m --for=condition=ready

  # Clean up NFS test resources from any previous failed attempt
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod \
    -n tenant-test --ignore-not-found --timeout=60s || true
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc \
    -n tenant-test --ignore-not-found --timeout=60s || true

  # Test RWX NFS mount in tenant cluster (uses kubevirt CSI driver with RWX support)
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nfs-test-pvc
  namespace: tenant-test
spec:
  accessModes:
  - ReadWriteMany
  storageClassName: kubevirt
  resources:
    requests:
      storage: 1Gi
EOF

  # Wait for PVC to be bound (RWX via kubevirt CSI provisions an NFS server pod, needs time)
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" wait pvc nfs-test-pvc -n tenant-test --timeout=3m --for=jsonpath='{.status.phase}'=Bound

  # Create Pod that writes and reads data from NFS volume
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: nfs-test-pod
  namespace: tenant-test
spec:
  containers:
  - name: test
    image: busybox
    command: ["sh", "-c", "echo 'nfs-mount-ok' > /data/test.txt && cat /data/test.txt"]
    volumeMounts:
    - name: nfs-vol
      mountPath: /data
  volumes:
  - name: nfs-vol
    persistentVolumeClaim:
      claimName: nfs-test-pvc
  restartPolicy: Never
EOF

  # 10m, not 5m: host CDI prime PVC + tenant CSI mount + busybox pull worst-case bursts past 5m.
  if ! kubectl --kubeconfig "tenantkubeconfig-${test_name}" wait pod nfs-test-pod -n tenant-test --timeout=10m --for=jsonpath='{.status.phase}'=Succeeded; then
    echo "=== NFS test pod did not complete ===" >&2
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" describe pod nfs-test-pod -n tenant-test >&2 || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" get events -n tenant-test --sort-by='.lastTimestamp' >&2 || true
    exit 1
  fi

  # Verify NFS data integrity
  nfs_result=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" logs nfs-test-pod -n tenant-test)
  if [ "$nfs_result" != "nfs-mount-ok" ]; then
    echo "NFS mount test failed: expected 'nfs-mount-ok', got '$nfs_result'" >&2
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod -n tenant-test --wait=false 2>/dev/null || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc -n tenant-test --wait=false 2>/dev/null || true
    exit 1
  fi

  # Cleanup NFS test resources in tenant cluster
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod -n tenant-test --wait
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc -n tenant-test

  # Wait for all machine deployment replicas to be ready (timeout after 10 minutes)
  kubectl wait machinedeployment kubernetes-${test_name}-md0 -n tenant-test --timeout=10m --for=jsonpath='{.status.v1beta2.readyReplicas}'=2

  for component in cilium coredns csi vsnap-crd; do
      kubectl wait hr "kubernetes-${test_name}-${component}" -n tenant-test --timeout=5m --for=condition=ready
    done
    kubectl wait hr "kubernetes-${test_name}-ingress-nginx" -n tenant-test --timeout=5m --for=condition=ready

  # Optional ouroboros addon assertions. Folded in from the standalone
  # ouroboros.bats so the test reuses this cluster instead of spinning up a
  # second ~25m Kamaji bringup. The assertions cover: HR Ready, controller
  # pod Running, Ingress->coredns-custom rewrite line injection, and the
  # end-to-end DNS resolution proof from inside the tenant cluster.
  if [ "${enable_ouroboros}" = "true" ]; then
    kubectl wait hr "kubernetes-${test_name}-ouroboros" -n tenant-test \
      --timeout=10m --for=condition=ready

    # cozystack coredns wrapper renders an empty coredns-custom ConfigMap in
    # kube-system; the ouroboros controller writes the rewrite snippet into
    # its ouroboros.override key.
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n kube-system \
      get configmap coredns-custom

    # Upstream chart ships no readiness probe — wait covers pod Running only;
    # the rewrite-snippet check below is the real reconciliation assertion.
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n cozy-ouroboros \
      wait pod --selector=app.kubernetes.io/component=controller \
      --timeout=5m --for=condition=ready

    local hairpin_host=hairpin-cozystack-e2e.example.invalid
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default apply -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: hairpin-probe
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - ${hairpin_host}
      secretName: hairpin-probe-tls
  rules:
    - host: ${hairpin_host}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: hairpin-probe
                port:
                  number: 80
EOF

    # Poll the import ConfigMap for the rewrite line. Dump-the-whole-map
    # form avoids the silent-empty kubectl jsonpath bracket-notation trap
    # on ConfigMap keys with dots (e.g. ouroboros.override).
    local deadline=$(( $(date +%s) + 300 ))
    local snippet=
    while [ "$(date +%s)" -lt "${deadline}" ]; do
      snippet=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n kube-system \
        get configmap coredns coredns-custom \
        -o 'jsonpath={range .items[*]}{.metadata.name}{"\n"}{.data}{"\n---\n"}{end}' \
        2>/dev/null || true)
      if echo "${snippet}" | grep -q "rewrite name ${hairpin_host}"; then break; fi
      sleep 5
    done
    if ! echo "${snippet}" | grep -q "rewrite name ${hairpin_host}"; then
      echo "ouroboros rewrite snippet for ${hairpin_host} not written to coredns-custom within 5m" >&2
      kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n cozy-ouroboros \
        logs --selector=app.kubernetes.io/component=controller --tail=200 --all-containers || true
      exit 1
    fi

    # End-to-end proof: resolve the hairpin host from inside the tenant.
    # CoreDNS reload-period default is 30s, so the in-pod loop is needed.
    local proxy_ip
    proxy_ip=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n cozy-ouroboros \
      get service ouroboros-proxy -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)
    if [ -z "${proxy_ip}" ]; then
      echo "ouroboros-proxy Service has no ClusterIP" >&2
      exit 1
    fi
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
      delete pod dnscheck --ignore-not-found 2>/dev/null || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
      run dnscheck --image=nicolaka/netshoot:v0.13 --restart=Never \
      --command -- sh -c "
        deadline=\$(( \$(date +%s) + 120 ))
        while [ \"\$(date +%s)\" -lt \"\${deadline}\" ]; do
          addr=\$(dig +short +tries=2 +time=5 ${hairpin_host} | head -n 1)
          echo \"resolved: \${addr:-<empty>}\"
          if [ \"\${addr}\" = \"${proxy_ip}\" ]; then
            exit 0
          fi
          sleep 5
        done
        echo \"timed out waiting for ${hairpin_host} to resolve to ${proxy_ip}\"
        exit 1
      "
    local dns_deadline=$(( $(date +%s) + 180 ))
    local phase=
    while [ "$(date +%s)" -lt "${dns_deadline}" ]; do
      phase=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
        get pod dnscheck -o jsonpath='{.status.phase}' 2>/dev/null || true)
      case "${phase}" in
        Succeeded|Failed) break ;;
      esac
      sleep 3
    done
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
      logs dnscheck 2>&1 | sed 's/^/  dnscheck: /' || true
    if [ "${phase:-}" != "Succeeded" ]; then
      echo "dnscheck pod did not reach Succeeded phase (last seen: ${phase:-<empty>})" >&2
      exit 1
    fi

    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
      delete pod dnscheck --ignore-not-found 2>/dev/null || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" -n default \
      delete ingress hairpin-probe --ignore-not-found 2>/dev/null || true
  fi

  # Wait for the parent kubernetes-${test_name} HR to be Ready before the
  # remediation guard runs. The guard reads `.status.history`, which is empty
  # until the helm install action completes — under Flux v2.8 kstatus the
  # parent's helm install can still be "Running 'install'" after every child
  # HR (cilium, coredns, csi, vsnap-crd, ingress-nginx) is already Ready,
  # because kstatus walks all applied resources before flipping the parent
  # Ready.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready

  # Guard: parent HelmRelease must not have entered an install/upgrade remediation cycle.
  # A non-zero installFailures/upgradeFailures indicates the helm-wait budget expired while
  # admin-kubeconfig was still being provisioned, which would trigger uninstall remediation
  # and churn the Cluster CR.
  # Flux helm-controller v2 retains per-revision release Snapshots in
  # .status.history; each Snapshot's .status reflects the Helm release
  # state (deployed/superseded/failed/uninstalled). A remediation cycle
  # leaves a "failed" or "uninstalled" entry behind that survives a later
  # successful reinstall, unlike the installFailures/upgradeFailures
  # counters (which ClearFailures zeroes on every successful reconcile).
  # The shape is pinned by hack/remediation-guard.bats; the upstream
  # types are github.com/fluxcd/helm-controller/api v2 Snapshot.
  history_statuses=$(kubectl get hr -n tenant-test "kubernetes-${test_name}" \
    -ojsonpath='{range .status.history[*]}{.status}{"\n"}{end}')
  # Always emit the raw value so a silent future-Flux field rename shows
  # up as "empty history on a Ready HR" in CI logs rather than vanishing.
  echo "Parent HelmRelease history statuses:"
  printf '%s\n' "${history_statuses:-<empty>}"
  if [ -z "${history_statuses}" ]; then
    echo "Unexpected empty .status.history on a Ready HelmRelease - Flux API shape may have changed." >&2
    kubectl -n tenant-test describe hr "kubernetes-${test_name}" >&2
    exit 1
  fi
  if helmrelease_has_remediation_cycle "${history_statuses}"; then
    echo "Parent HelmRelease entered remediation cycle." >&2
    kubectl -n tenant-test describe hr "kubernetes-${test_name}" >&2
    exit 1
  fi

  # Success: disarm the tenant-snapshot trap so it doesn't fire on the clean exit.
  trap - EXIT
  # Clean up: delete the test-scoped tenant API LoadBalancer (frees its MetalLB
  # IP) and the local kubeconfig.
  kubectl -n tenant-test delete service "kubernetes-${test_name}-e2e-lb" --ignore-not-found --wait=false 2>/dev/null || true
  rm -f "tenantkubeconfig-${test_name}"
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io "${test_name}" --ignore-not-found --wait=false 2>/dev/null || true

}
