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
      valuesOverride: {}
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
  kubectl --namespace "${ns}" wait \
    helmrelease "kubernetes-${cluster}-ouroboros" \
    --timeout=25m --for=condition=ready

  # Capture the tenant admin-kubeconfig so subsequent kubectls hit the
  # tenant apiserver, not the host. Tmp file is removed on exit of this
  # @test body — cozytest does not invoke teardown().
  kubeconfig=$(mktemp)
  trap "rm -f ${kubeconfig}" EXIT
  kubectl --namespace "${ns}" get secret \
    "kubernetes-${cluster}-admin-kubeconfig" \
    --output jsonpath='{.data.super-admin\.svc}' \
    | base64 --decode > "${kubeconfig}"

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

  deadline=$(( $(date +%s) + 180 ))
  snippet=
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    # jsonpath bracket notation tolerates dotted keys AND missing
    # `.data` (returns empty stdout, exit 0). The earlier go-template
    # form `{{ index .data "ouroboros.override" }}` writes a multi-KB
    # "Error executing template ... index of untyped nil" diagnostic
    # to STDOUT (not stderr) when `.data` is missing, poisoning the
    # grep below.
    snippet=$(KUBECONFIG="${kubeconfig}" kubectl --namespace kube-system \
      get configmap coredns-custom \
      --output "jsonpath={.data['ouroboros.override']}" 2>/dev/null || true)
    if echo "${snippet}" | grep -q "rewrite name ${hairpin_host}"; then
      break
    fi
    sleep 5
  done
  echo "${snippet}" | grep -q "rewrite name ${hairpin_host}"

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
  [ -n "${proxy_ip}" ]

  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
    delete pod dnscheck --ignore-not-found 2>/dev/null || true
  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
    run dnscheck --image=nicolaka/netshoot:v0.13 --restart=Never \
    --command -- sh -c "
      set -e
      addr=\$(dig +short +tries=2 +time=5 ${hairpin_host} | head -n 1)
      echo \"resolved: \${addr:-<empty>}\"
      [ \"\${addr}\" = \"${proxy_ip}\" ]
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
  [ "${phase:-}" = "Succeeded" ]

  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
    delete pod dnscheck --ignore-not-found 2>/dev/null || true
  KUBECONFIG="${kubeconfig}" kubectl --namespace default \
    delete ingress hairpin-probe --ignore-not-found 2>/dev/null || true
  kubectl --namespace "${ns}" delete kuberneteses.apps.cozystack.io \
    "${cluster}" --ignore-not-found --wait=false 2>/dev/null || true
  rm -f "${kubeconfig}"
}
