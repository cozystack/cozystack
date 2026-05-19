#!/usr/bin/env bats

# Smoke-test the kuberture system addon: a Package CR for cozystack.kuberture
# with config.outputs configured must materialise an HR in cozy-kuberture,
# the controller Deployment must roll out, and the controller must create the
# configured headless Services with the right external-dns annotations
# (hostname/target/ttl) reflecting the IPs from default/kubernetes
# EndpointSlice. Two outputs with different annotationPrefix values exercise
# the cozystack-relevant property: a single kuberture instance can address
# multiple external-dns instances from one platform-level package.
#
# cozytest.sh is plain POSIX shell — no `[[ ... ]]`, no bats `run` helper,
# no FD 3, and `teardown()` is never auto-invoked. Cleanup is inlined at the
# end of the @test body and runs on the success path only; the e2e Makefile's
# sandbox teardown handles the failure case.

# Shared cleanup so the inline success-path call at the end of the @test
# body and the teardown hook (alive only under upstream bats) cannot
# drift apart.
_cleanup() {
  # Probe Pods and their RBAC live outside the chart and need explicit
  # teardown — the Package delete cascade does not own them. The
  # ClusterRole/ClusterRoleBinding are cluster-scoped and would otherwise
  # leak across e2e re-runs on the same sandbox cluster.
  kubectl delete --namespace cozy-kuberture --ignore-not-found pod/kuberture-e2e-edns-public pod/kuberture-e2e-edns-internal 2>/dev/null || true
  kubectl delete --namespace cozy-kuberture --ignore-not-found sa/kuberture-e2e-edns-probe 2>/dev/null || true
  kubectl delete --ignore-not-found clusterrolebinding/kuberture-e2e-edns-probe clusterrole/kuberture-e2e-edns-probe 2>/dev/null || true
  kubectl delete package.cozystack.io cozystack.kuberture --ignore-not-found --wait=true --timeout=180s 2>/dev/null || true
}

teardown() {
  # Dead code under cozytest.sh — the runner never invokes it. Kept so
  # the file still works under upstream bats (e.g. for local debugging
  # via `bats hack/e2e-apps/kuberture.bats`).
  _cleanup
}

@test "Platform-level kuberture publishes default/kubernetes EndpointSlice as annotated headless Services for multiple external-dns instances" {
  pub_host=kuberture-e2e-public.cozystack.test
  int_host=kuberture-e2e-internal.cozystack.test
  pub_prefix=external-dns.alpha.kubernetes.io/
  int_prefix=kuberture-e2e-internal-dns.io/

  kubectl apply --filename - <<EOF
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.kuberture
spec:
  variant: default
  components:
    kuberture:
      values:
        kuberture:
          config:
            outputs:
              - name: public
                hostname:
                  - ${pub_host}
                annotationPrefix: ${pub_prefix}
                serviceName: kuberture-e2e-public
                addressSource: endpointslice
                recordTTL: 60
              - name: internal
                hostname:
                  - ${int_host}
                annotationPrefix: ${int_prefix}
                serviceName: kuberture-e2e-internal
                addressSource: endpointslice
                recordTTL: 300
EOF

  # The cozystack operator materialises the namespace + HR from the Package;
  # kubectl wait errors immediately if the object is not present yet, so loop
  # until it appears before waiting on its condition.
  timeout 180 sh -ec 'until kubectl get namespace cozy-kuberture >/dev/null 2>&1; do sleep 2; done'
  timeout 180 sh -ec 'until kubectl --namespace cozy-kuberture get hr kuberture >/dev/null 2>&1; do sleep 2; done'
  kubectl --namespace cozy-kuberture wait hr kuberture --timeout=300s --for=condition=ready

  # Controller rollout. Use kubectl wait so the readiness check is anchored on
  # the Available condition and not on a substring match of the replica count.
  kubectl --namespace cozy-kuberture wait deployment kuberture --for=condition=available --timeout=180s

  # The output Services are created reactively after the controller observes
  # default/kubernetes EndpointSlice. Wait for both to appear.
  for svc in kuberture-e2e-public kuberture-e2e-internal; do
    timeout 120 sh -ec "until kubectl --namespace cozy-kuberture get service ${svc} >/dev/null 2>&1; do sleep 3; done"
  done

  # Both Services must be headless and carry external-dns annotations under
  # their configured prefix. The `target` annotation must be non-empty —
  # the actual IPs come from default/kubernetes EndpointSlice and depend on
  # the cluster, so we only assert presence.
  pub_json=$(kubectl --namespace cozy-kuberture get service kuberture-e2e-public --output json)
  int_json=$(kubectl --namespace cozy-kuberture get service kuberture-e2e-internal --output json)

  test "$(echo "${pub_json}" | jq --raw-output '.spec.clusterIP')" = "None"
  test "$(echo "${int_json}" | jq --raw-output '.spec.clusterIP')" = "None"

  test "$(echo "${pub_json}" | jq --raw-output --arg k "${pub_prefix}hostname" '.metadata.annotations[$k]')" = "${pub_host}"
  test "$(echo "${int_json}" | jq --raw-output --arg k "${int_prefix}hostname" '.metadata.annotations[$k]')" = "${int_host}"

  pub_target=$(echo "${pub_json}" | jq --raw-output --arg k "${pub_prefix}target" '.metadata.annotations[$k] // ""')
  int_target=$(echo "${int_json}" | jq --raw-output --arg k "${int_prefix}target" '.metadata.annotations[$k] // ""')
  test -n "${pub_target}"
  test -n "${int_target}"

  test "$(echo "${pub_json}" | jq --raw-output --arg k "${pub_prefix}ttl" '.metadata.annotations[$k]')" = "60"
  test "$(echo "${int_json}" | jq --raw-output --arg k "${int_prefix}ttl" '.metadata.annotations[$k]')" = "300"

  # The cross-prefix assertion proves the per-output isolation: the public
  # Service must NOT carry the internal prefix's annotation, and vice versa.
  test "$(echo "${pub_json}" | jq --raw-output --arg k "${int_prefix}hostname" '.metadata.annotations[$k] // "absent"')" = "absent"
  test "$(echo "${int_json}" | jq --raw-output --arg k "${pub_prefix}hostname" '.metadata.annotations[$k] // "absent"')" = "absent"

  # Routing proof: run two short-lived external-dns probes, each with a
  # distinct --annotation-prefix, and verify each one picks up exactly its
  # matching Service. This is the upstream Split Horizon DNS pattern from
  # kubernetes-sigs/external-dns docs/advanced/split-horizon. inmemory
  # provider + noop registry + --once means each probe runs one sync cycle
  # against the live Services in cozy-kuberture, logs what it would publish,
  # and exits — the logs are the assertion surface.
  # external-dns 0.20 needs cluster-wide read on services/endpoints/nodes/
  # namespaces even when `--namespace` narrows the source scope (informer
  # cache initialisation reads cluster-wide before applying the filter).
  # A namespace-scoped Role surfaces as a Pod crash without an obvious
  # error in the bats log, so use ClusterRole + ClusterRoleBinding here.
  kubectl apply --filename - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kuberture-e2e-edns-probe
  namespace: cozy-kuberture
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kuberture-e2e-edns-probe
rules:
  - apiGroups: [""]
    resources: ["services", "endpoints", "pods", "nodes", "namespaces"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kuberture-e2e-edns-probe
subjects:
  - kind: ServiceAccount
    name: kuberture-e2e-edns-probe
    namespace: cozy-kuberture
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kuberture-e2e-edns-probe
EOF

  # One Pod per probe — explicit instead of eval-indirection. cozytest.sh runs
  # the body under a POSIX `sh` (not bash) and `${probe}_prefix` indirection
  # via eval is brittle (a name mismatch produces "parameter not set" with no
  # useful diagnostic). Two near-identical blocks read more clearly here.
  kubectl apply --filename - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: kuberture-e2e-edns-public
  namespace: cozy-kuberture
spec:
  serviceAccountName: kuberture-e2e-edns-probe
  restartPolicy: Never
  containers:
    - name: edns
      image: registry.k8s.io/external-dns/external-dns:v0.20.0
      args:
        - --source=service
        - --namespace=cozy-kuberture
        - --provider=inmemory
        - --inmemory-zone=cozystack.test
        - --registry=noop
        - --once
        - --interval=10s
        - --annotation-prefix=${pub_prefix}
        - --domain-filter=cozystack.test
        - --log-level=info
        - --log-format=text
---
apiVersion: v1
kind: Pod
metadata:
  name: kuberture-e2e-edns-internal
  namespace: cozy-kuberture
spec:
  serviceAccountName: kuberture-e2e-edns-probe
  restartPolicy: Never
  containers:
    - name: edns
      image: registry.k8s.io/external-dns/external-dns:v0.20.0
      args:
        - --source=service
        - --namespace=cozy-kuberture
        - --provider=inmemory
        - --inmemory-zone=cozystack.test
        - --registry=noop
        - --once
        - --interval=10s
        - --annotation-prefix=${int_prefix}
        - --domain-filter=cozystack.test
        - --log-level=info
        - --log-format=text
EOF

  for probe in public internal; do
    timeout 180 sh -ec "until kubectl --namespace cozy-kuberture get pod kuberture-e2e-edns-${probe} -o jsonpath='{.status.phase}' 2>/dev/null | grep -E '^(Succeeded|Failed)$' >/dev/null; do sleep 3; done"
    phase=$(kubectl --namespace cozy-kuberture get pod kuberture-e2e-edns-${probe} -o jsonpath='{.status.phase}')
    if [ "${phase}" != "Succeeded" ]; then
      # Dump enough context to diagnose the failure from the bats log alone
      # (the e2e sandbox is destroyed before the run artefacts ship, so
      # post-mortem kubectl access on the cluster is not available).
      echo "==== probe ${probe} failed: phase=${phase}; describe ===="
      kubectl --namespace cozy-kuberture describe pod kuberture-e2e-edns-${probe} || true
      echo "==== probe ${probe} logs (current container) ===="
      kubectl --namespace cozy-kuberture logs pod/kuberture-e2e-edns-${probe} || true
      echo "==== probe ${probe} logs (previous container, if any) ===="
      kubectl --namespace cozy-kuberture logs pod/kuberture-e2e-edns-${probe} --previous || true
      exit 1
    fi
  done

  pub_logs=$(kubectl --namespace cozy-kuberture logs pod/kuberture-e2e-edns-public)
  int_logs=$(kubectl --namespace cozy-kuberture logs pod/kuberture-e2e-edns-internal)

  # The probe that reads the default external-dns prefix must see
  # kuberture-e2e-public.cozystack.test (kuberture-e2e-public Service carries
  # external-dns.alpha.kubernetes.io/hostname=...) and must NOT see the
  # internal hostname (kuberture-e2e-internal carries only the internal prefix).
  echo "${pub_logs}" | grep -F "${pub_host}" >/dev/null
  ! echo "${pub_logs}" | grep -F "${int_host}" >/dev/null

  # And the inverse for the internal-prefix probe.
  echo "${int_logs}" | grep -F "${int_host}" >/dev/null
  ! echo "${int_logs}" | grep -F "${pub_host}" >/dev/null

  # _cleanup deletes the probe Pods, their SA, the ClusterRole+ClusterRoleBinding,
  # and finally the Package CR. Inlined here for the success path so a re-run
  # within the same e2e session starts clean; failure path is covered by the
  # sandbox teardown.
  _cleanup
}
