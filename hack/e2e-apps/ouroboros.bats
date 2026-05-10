#!/usr/bin/env bats

# Smoke-test the ouroboros tenant addon: a tenant Kubernetes cluster that
# enables addons.ingressNginx + addons.ouroboros must end up with the
# cozy-ouroboros HelmRelease ready, controller and proxy Deployments
# rolled out, and an empty kube-system/coredns-custom ConfigMap created
# by the cozystack coredns wrapper. After creating an Ingress with a TLS
# host the controller must write a `rewrite name <host>` line into the
# ouroboros.override key of the import ConfigMap — without that proof
# of reconciliation, pod-readiness alone is meaningless (the upstream
# chart ships no readiness probes).
#
# Hairpin-fix end-to-end (PROXY-protocol header roundtrip) is covered by
# the upstream lexfrei/ouroboros e2e matrix on every push to that repo;
# this file is the cozystack-side integration test that the addon wires
# up and reconciles correctly.
#
# cozytest.sh is plain POSIX shell — no `[[ ... ]]`, no bats `run`
# helper, no FD 3 (the runner does not open it), and `teardown()` is
# never auto-invoked. Cleanup is inlined at the end of the @test body
# and runs on the success path only; the e2e Makefile's sandbox
# teardown handles the failure case.

teardown() {
  # Dead code under cozytest.sh — the runner never invokes it. Kept so
  # the file still works under upstream bats (e.g. for local debugging
  # via `bats hack/e2e-apps/ouroboros.bats`).
  kubectl --namespace tenant-test delete kuberneteses.apps.cozystack.io \
    test-ouroboros --ignore-not-found --wait=false 2>/dev/null || true
}

@test "Tenant Kubernetes cluster with addons.ouroboros.enabled rolls out and reconciles" {
  cluster=test-ouroboros
  ns=tenant-test
  hairpin_host=hairpin-cozystack-e2e.example.invalid

  kubectl apply --filename - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: "${cluster}"
  namespace: ${ns}
spec:
  addons:
    ingressNginx:
      enabled: true
      hosts: []
      valuesOverride: {}
    ouroboros:
      enabled: true
      # logLevel=debug surfaces per-event informer activity
      # (AddFunc/UpdateFunc/DeleteFunc with kind+namespace/name) and
      # reconcile pacing on this test tenant. Scoped to the bats fixture
      # only — production tenants stay at the upstream chart default
      # (info) per the cozystack "default for low-skill operator"
      # stance. The verbose logs end up in dump_tenant_state when an
      # assertion fails, which is exactly when extra observability is
      # worth its cost.
      valuesOverride:
        ouroboros:
          controller:
            logLevel: debug
  controlPlane:
    apiServer:
      resources: {}
      resourcesPreset: small
    controllerManager:
      resources: {}
      resourcesPreset: micro
  nodeGroups:
    md0:
      minReplicas: 2
      maxReplicas: 2
      instanceType: u1.medium
      diskSize: 20Gi
      roles:
        - ingress-nginx
EOF

  # The HelmRelease that apps/kubernetes/templates/helmreleases/ouroboros.yaml
  # renders is named <release>-ouroboros in the tenant's host namespace.
  # `dependsOn` chains through the parent kubernetes HelmRelease, then
  # cilium, coredns, and ingress-nginx — and the parent itself can only
  # go Ready after Kamaji boots the tenant control plane (~4m), tcp-balancer
  # is reachable (~5m), worker MachineDeployment scales (~8m), and
  # tenant-side Deployments roll out (~4m). The cumulative budget on the
  # current QEMU runners is around 25m for that path, so the timeout has
  # to leave headroom for that bringup before the addon HR even starts
  # reconciling.
  # Extract the tenant admin-kubeconfig early so the failure-path
  # diagnostic block below can inspect tenant-cluster state. The
  # kubeconfig secret is created by the parent kubernetes HR's Kamaji
  # bringup, well before the ouroboros HR starts reconciling — by the
  # time ouroboros HR can fail the secret is reliably present. The
  # extra wait on the parent HR upper-bounds the case where the parent
  # itself never becomes Ready (the secret never appears, the kubectl
  # below would fail with a misleading "no resources found").
  #
  # Use `super-admin.conf` (NOT `super-admin.svc`) and rewrite the
  # server URL to `https://localhost:<port>` then start a kubectl
  # port-forward to the tenant apiserver Service in the host namespace.
  # The `super-admin.svc` variant points at the in-cluster Service DNS
  # name `<release>.<ns>.svc:6443`, which the e2e sandbox container
  # cannot resolve — its resolver is the Docker/QEMU host link-local
  # 169.254.169.254, not host-cluster CoreDNS, so every tenant-side
  # kubectl call returns `no such host`. The port-forward + localhost
  # rewrite is the same pattern used by hack/e2e-apps/run-kubernetes.sh.
  kubeconfig=$(mktemp)
  pf_port=59999
  cleanup_kubeconfig() {
    pkill -f "port-forward.*service/kubernetes-${cluster}.*${pf_port}:" 2>/dev/null || true
    rm -f "${kubeconfig}"
  }
  # Host-side state dump used on the parent-HR failure path, where the
  # kubeconfig has not yet been extracted (so dump_tenant_state's
  # KUBECONFIG-driven calls would all hit an empty/missing file). Tightly
  # scoped to the resources a kamaji-bringup hang surfaces on:
  # - the Kubernetes apps.cozystack.io CR (status conditions show what the
  #   parent helm-controller is waiting on);
  # - the parent kubernetes-${cluster} HelmRelease (its conditions show
  #   the chart-render or apply state);
  # - the Kamaji TenantControlPlane (TCP) — kamaji ships its own status
  #   conditions when etcd, certs, or konnectivity fail to come up.
  dump_host_state() {
    echo "=== ouroboros host-side diagnostics (pre-kubeconfig) ==="
    echo "--- host: Kubernetes apps.cozystack.io describe ---"
    kubectl --namespace "${ns}" describe \
      kuberneteses.apps.cozystack.io "${cluster}" || true
    echo "--- host: parent HelmRelease describe ---"
    kubectl --namespace "${ns}" describe \
      helmrelease "kubernetes-${cluster}" || true
    echo "--- host: Kamaji TenantControlPlane describe ---"
    kubectl --namespace "${ns}" describe \
      tenantcontrolplane.kamaji.clastix.io "kubernetes-${cluster}" || true
    # Kamaji-managed apiserver pods live in the host ${ns} as a regular
    # Deployment. cozyreport does NOT collect logs from this namespace by
    # default (only cozy-* host namespaces), so without this dump there
    # is no record of the actual kube-apiserver behaviour at the moment
    # the bats poll loop was reading stale ConfigMap data. Pull pod-list
    # for replica visibility, then per-pod logs of kube-apiserver +
    # konnectivity-server (the two containers a kamaji apiserver pod
    # ships) so a future watch-cache-lag investigation can correlate
    # bats kubectl `get configmap` timestamps against apiserver
    # request-handling on each replica.
    echo "--- host: Kamaji apiserver pods (-o wide) ---"
    kubectl --namespace "${ns}" get pods \
      --selector "kamaji.clastix.io/name=kubernetes-${cluster}" \
      --output wide || true
    echo "--- host: kube-apiserver logs from each Kamaji replica (tail=400) ---"
    kubectl --namespace "${ns}" logs \
      --selector "kamaji.clastix.io/name=kubernetes-${cluster}" \
      --container kube-apiserver \
      --tail=400 --prefix=true --all-containers=false || true
    echo "--- host: konnectivity-server logs from each Kamaji replica (tail=200) ---"
    kubectl --namespace "${ns}" logs \
      --selector "kamaji.clastix.io/name=kubernetes-${cluster}" \
      --container konnectivity-server \
      --tail=200 --prefix=true --all-containers=false || true
  }
  # Tenant-side state dump used both on the HR-not-Ready failure path and
  # on every later assertion (rewrite snippet missing, dnscheck pod
  # not Succeeded, etc.). Without one centralised dump every assertion
  # would either need its own copy of the diagnostic block, or fail
  # opaquely under set -e the moment the assertion command returns
  # non-zero. Wrap each assertion in `if ! ...; then dump_tenant_state;
  # exit 1; fi` so cozytest captures actionable tenant-side state on
  # the failure that triggered the exit, regardless of which one fired.
  #
  # Both dump_host_state and dump_tenant_state are intentionally local
  # to this @test (not setup()-defined) — cozytest.sh does not invoke
  # bats setup/teardown, and the helpers reference @test-local variables
  # (cluster, ns, kubeconfig). If a second @test ever lands in this file,
  # hoist the helpers and parametrise on those variables.
  dump_tenant_state() {
    # Pull host-side state too (Kamaji apiserver pod logs in particular):
    # the singular-GET-vs-LIST staleness gap and any candidate watch-cache
    # /etcd-watch lag have to be diagnosed against the actual apiserver
    # access log, not the tenant-side surface alone. Calling dump_host_state
    # first keeps host context in front of tenant context in the captured
    # output, which matches the request flow (host kubectl → kamaji
    # apiserver → tenant cluster).
    dump_host_state
    echo "=== ouroboros tenant-side diagnostics ==="
    echo "--- host: HelmRelease describe ---"
    kubectl --namespace "${ns}" describe \
      helmrelease "kubernetes-${cluster}-ouroboros" || true
    echo "--- tenant: cozy-ouroboros pods (-o wide) ---"
    KUBECONFIG="${kubeconfig}" kubectl --namespace cozy-ouroboros \
      get pods --output wide || true
    echo "--- tenant: cozy-ouroboros pod descriptions ---"
    KUBECONFIG="${kubeconfig}" kubectl --namespace cozy-ouroboros \
      describe pods || true
    echo "--- tenant: cozy-ouroboros recent events ---"
    KUBECONFIG="${kubeconfig}" kubectl --namespace cozy-ouroboros \
      get events --sort-by=.lastTimestamp || true
    echo "--- tenant: cozy-ouroboros controller logs ---"
    KUBECONFIG="${kubeconfig}" kubectl --namespace cozy-ouroboros \
      logs --selector=app.kubernetes.io/component=controller \
      --tail=200 --prefix=true --all-containers || true
    echo "--- tenant: cozy-ouroboros proxy logs ---"
    KUBECONFIG="${kubeconfig}" kubectl --namespace cozy-ouroboros \
      logs --selector=app.kubernetes.io/component=proxy \
      --tail=200 --prefix=true --all-containers || true
    echo "--- tenant: cozy-ingress-nginx pods/svc/endpoints ---"
    KUBECONFIG="${kubeconfig}" kubectl --namespace cozy-ingress-nginx \
      get pods,svc,endpoints --output wide || true
    echo "--- tenant: kube-system coredns Corefile + custom ---"
    KUBECONFIG="${kubeconfig}" kubectl --namespace kube-system \
      get configmap coredns coredns-custom \
      --output jsonpath='{range .items[*]}{.metadata.name}{"\n"}{.data}{"\n---\n"}{end}' || true
    echo "--- tenant: default ns Ingresses ---"
    KUBECONFIG="${kubeconfig}" kubectl --namespace default \
      get ingress --output yaml || true
  }
  trap cleanup_kubeconfig EXIT
  # Wrap parent kubernetes-HR wait in dump_host_state so a Kamaji bringup
  # wedge does not surface as opaque `wait: timed out`. Tenant-side
  # diagnostics are unavailable here (no kubeconfig yet); the host-side
  # dump covers parent HR conditions, the Kubernetes CR, and the TCP.
  if ! kubectl --namespace "${ns}" wait \
       helmrelease "kubernetes-${cluster}" \
       --timeout=25m --for=condition=ready; then
    dump_host_state
    exit 1
  fi
  kubectl --namespace "${ns}" get secret \
    "kubernetes-${cluster}-admin-kubeconfig" \
    --output jsonpath='{.data.super-admin\.conf}' \
    | base64 --decode > "${kubeconfig}"
  yq -i ".clusters[0].cluster.server = \"https://localhost:${pf_port}\"" "${kubeconfig}"
  pkill -f "port-forward.*service/kubernetes-${cluster}.*${pf_port}:" 2>/dev/null || true
  kubectl --namespace "${ns}" port-forward \
    "service/kubernetes-${cluster}" "${pf_port}":6443 > /dev/null 2>&1 &
  timeout 30 sh -ec 'until curl -sk https://localhost:'"${pf_port}"' >/dev/null 2>&1; do sleep 1; done'

  # Wait for the addon HR. On failure, dump tenant-side diagnostics
  # before exiting — cozyreport stops at host scope and gives no
  # visibility into the tenant control plane, so without this dump
  # CI failures show up as opaque `wait: timed out` with no actionable
  # signal about why the proxy Deployment never reaches Ready.
  if ! kubectl --namespace "${ns}" wait \
       helmrelease "kubernetes-${cluster}-ouroboros" \
       --timeout=20m --for=condition=ready; then
    dump_tenant_state
    exit 1
  fi

  # The cozystack coredns wrapper renders an empty coredns-custom
  # ConfigMap in kube-system (with the helm.sh/resource-policy: keep
  # annotation), and the CoreDNS Deployment's Corefile imports
  # /etc/coredns/custom/*.override.
  KUBECONFIG="${kubeconfig}" kubectl --namespace kube-system \
    get configmap coredns-custom

  # Pod-readiness is true the moment the container Runs, regardless of
  # whether the controller actually reconciles (the upstream chart
  # ships no readiness probes). The next step is the real assertion.
  KUBECONFIG="${kubeconfig}" kubectl --namespace cozy-ouroboros \
    wait pod --selector=app.kubernetes.io/component=controller \
    --timeout=5m --for=condition=ready

  # Create an Ingress with a TLS host and poll the import ConfigMap
  # for a matching rewrite line. Anything else (CRD missing, gateway-api
  # gate misfiring, RBAC narrowed wrong) shows up here rather than as a
  # vacuously-passing pod-ready check.
  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
    apply --filename - <<EOF
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

  deadline=$(( $(date +%s) + 900 ))
  snippet=
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    # Dump the whole `.data` map and grep the rewrite line out of it,
    # rather than extracting a single key via jsonpath bracket-notation
    # (`{.data['ouroboros.override']}`). The bracket-notation form is a
    # silent-empty kubectl jsonpath trap for ConfigMap keys with a dot
    # in them: the parser reads the bracket as an array index, single-
    # quoted "ouroboros.override" is not a valid numeric index, and
    # rather than erroring the result is `""`. Confirmed locally on
    # kind v1.35.0 against a ConfigMap whose only data key is
    # `ouroboros.override` — `-o yaml` shows the value, `-o jsonpath=
    # {.data}` returns the whole map, but `-o jsonpath={.data
    # ['ouroboros.override']}` returns an empty string with exit 0.
    # The `{range .items[*]}…{.data}` shape avoids the trap because it
    # emits the full map, which still contains the rewrite line that
    # grep matches.
    snippet=$(KUBECONFIG="${kubeconfig}" kubectl --namespace kube-system \
      get configmap coredns coredns-custom \
      --output 'jsonpath={range .items[*]}{.metadata.name}{"\n"}{.data}{"\n---\n"}{end}' \
      2>/dev/null || true)
    if echo "${snippet}" | grep -q "rewrite name ${hairpin_host}"; then
      break
    fi
    sleep 5
  done
  if ! echo "${snippet}" | grep -q "rewrite name ${hairpin_host}"; then
    echo "rewrite snippet for ${hairpin_host} not written to coredns-custom within deadline"
    dump_tenant_state
    exit 1
  fi

  # Beyond "rewrite written to ConfigMap", verify CoreDNS actually
  # serves the rewrite — otherwise an `import` directive misconfig,
  # missing `reload` plugin, or stuck CoreDNS pod would let this test
  # pass vacuously while in-cluster DNS for hairpinned hosts still
  # returns NXDOMAIN. Resolve from inside the tenant; assert the
  # answer matches the ouroboros-proxy ClusterIP. Skip the curl/TLS
  # roundtrip — that would need cert-manager + a real workload, out
  # of scope for this bats; the DNS step alone catches the wiring
  # failures that the rewrite-line check cannot see.
  proxy_ip=$(KUBECONFIG="${kubeconfig}" kubectl --namespace cozy-ouroboros \
    get service ouroboros-proxy --output jsonpath='{.spec.clusterIP}' 2>/dev/null || true)
  if [ -z "${proxy_ip}" ]; then
    echo "ouroboros-proxy Service has no ClusterIP"
    dump_tenant_state
    exit 1
  fi

  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
    delete pod dnscheck --ignore-not-found 2>/dev/null || true
  # The dig+compare runs inside the pod in a retry loop. CoreDNS reload-period
  # default is 30s — without an in-pod retry the pod could exit Failed before
  # the rewrite snippet was actually loaded by CoreDNS, even though the
  # ConfigMap already has it. The poll on the pod phase below is also bounded,
  # but it only retries the *pod*; the dig itself ran once. Loop in-pod for up
  # to 120s, sleep 5s between attempts, exit 0 the moment a match lands.
  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
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
  dns_deadline=$(( $(date +%s) + 180 ))
  while [ "$(date +%s)" -lt "${dns_deadline}" ]; do
    phase=$(KUBECONFIG="${kubeconfig}" kubectl --namespace default \
      get pod dnscheck --output jsonpath='{.status.phase}' 2>/dev/null || true)
    case "${phase}" in
      Succeeded|Failed) break ;;
    esac
    sleep 3
  done
  KUBECONFIG="${kubeconfig}" kubectl --namespace default logs dnscheck 2>&1 | sed 's/^/  dnscheck: /' || true
  if [ "${phase:-}" != "Succeeded" ]; then
    echo "dnscheck pod did not reach Succeeded phase (last seen: ${phase:-<empty>})"
    dump_tenant_state
    exit 1
  fi

  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
    delete pod dnscheck --ignore-not-found 2>/dev/null || true
  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
    delete ingress hairpin-probe --ignore-not-found 2>/dev/null || true
  kubectl --namespace "${ns}" delete kuberneteses.apps.cozystack.io \
    "${cluster}" --ignore-not-found --wait=false 2>/dev/null || true
  cleanup_kubeconfig
}
