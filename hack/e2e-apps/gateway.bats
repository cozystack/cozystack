#!/usr/bin/env bats

@test "Gateway API CRDs are installed and the cilium GatewayClass is Accepted" {
  # Gateway API CRDs must exist — installed by packages/system/gateway-api-crds
  kubectl wait crd/gatewayclasses.gateway.networking.k8s.io --for=condition=Established --timeout=60s
  kubectl wait crd/gateways.gateway.networking.k8s.io --for=condition=Established --timeout=60s
  kubectl wait crd/httproutes.gateway.networking.k8s.io --for=condition=Established --timeout=60s

  # Cilium must have registered its built-in GatewayClass once gatewayAPI.enabled
  # is true in the cilium values. This verifies the flip in
  # packages/system/cilium/values.yaml propagated end-to-end.
  timeout 120 sh -ec 'until kubectl get gatewayclass cilium >/dev/null 2>&1; do sleep 2; done'
  kubectl wait gatewayclass/cilium --for=condition=Accepted --timeout=3m
}

@test "Cilium Gateway API controller reconciles a minimal Gateway to Programmed" {
  # Use the pre-existing tenant-test namespace created by e2e-install-cozystack.bats.
  kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gateway-e2e-probe
  namespace: tenant-test
spec:
  gatewayClassName: cilium
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    allowedRoutes:
      namespaces:
        from: Same
EOF

  # The controller must accept and program the Gateway.
  kubectl -n tenant-test wait gateway/gateway-e2e-probe --for=condition=Accepted --timeout=2m
  kubectl -n tenant-test wait gateway/gateway-e2e-probe --for=condition=Programmed --timeout=3m

  # Cilium materialises a LoadBalancer Service named cilium-gateway-<gateway-name>
  # for each programmed Gateway. Its existence is the observable proof that the
  # full data-plane wiring kicked in.
  kubectl -n tenant-test get svc cilium-gateway-gateway-e2e-probe

  # Cleanup
  kubectl -n tenant-test delete gateway/gateway-e2e-probe --ignore-not-found --timeout=1m
}

@test "cozystack-gateway-hostname-policy VAP and binding are installed" {
  # Diagnose the hostname-policy enforcement path from the ground up.
  # If these resources are missing, admission cannot reject anything.
  kubectl get validatingadmissionpolicy cozystack-gateway-hostname-policy -o yaml
  kubectl get validatingadmissionpolicybinding cozystack-gateway-hostname-policy -o yaml
  # Binding MUST have validationActions: [Deny] — [Audit] / [] would let requests through silently.
  actions=$(kubectl get validatingadmissionpolicybinding cozystack-gateway-hostname-policy -o jsonpath='{.spec.validationActions}')
  echo "binding.validationActions=$actions"
  case "$actions" in
    *Deny*) ;;
    *) echo "SETUP FAILURE: binding.validationActions lacks Deny (got '$actions')" >&2; return 1 ;;
  esac
}

@test "tenant-test namespace carries namespace.cozystack.io/host label from tenant chart" {
  # Diagnostic: the whole hostname-policy VAP keys off this label. If it is
  # missing or empty, the VAP's matchCondition returns false, VAP skips, and
  # EVERY Gateway in the namespace is admitted regardless of listener hostname.
  # Make that bug loud instead of letting it fall through as a silent pass.
  host_label=$(kubectl get namespace tenant-test -o jsonpath='{.metadata.labels.namespace\.cozystack\.io/host}')
  if [ -z "$host_label" ]; then
    echo "SETUP FAILURE: tenant-test namespace lacks namespace.cozystack.io/host label" >&2
    kubectl get namespace tenant-test -o yaml >&2
    return 1
  fi
  if [ "$host_label" != "test.example.org" ]; then
    echo "SETUP FAILURE: tenant-test host label is '$host_label', expected 'test.example.org'" >&2
    kubectl get namespace tenant-test -o yaml >&2
    return 1
  fi
}

@test "ValidatingAdmissionPolicy rejects Gateway with foreign hostname" {
  # tenant-test namespace should only be allowed to publish its own
  # domain suffix ('.test.example.org'); a listener hostname from the
  # root tenant's apex must be denied by cozystack-gateway-hostname-policy.
  # hack/cozytest.sh is /bin/sh (dash) with set -e — a failing command
  # substitution propagates its exit status through variable assignment and
  # kills the test. We specifically EXPECT admission to reject the apply, so
  # use `if !` — set -e is disabled inside the if-condition, the exit status
  # is captured, and $output is filled either way for the follow-up greps.
  if ! output=$(kubectl apply -f - 2>&1 <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: hostname-hijack-probe
  namespace: tenant-test
spec:
  gatewayClassName: cilium
  listeners:
  - name: https
    protocol: HTTPS
    port: 443
    hostname: "dashboard.example.org"
    tls:
      mode: Terminate
      certificateRefs:
      - name: noop
    allowedRoutes:
      namespaces:
        from: Same
EOF
); then
    # Happy path: admission rejected the Gateway. Verify the rejection came
    # from our VAP and names the expected tenant host.
    echo "$output" | grep -qi "ValidatingAdmissionPolicy"
    echo "$output" | grep -q "must equal test.example.org"
  else
    echo "BUG: admission accepted cross-tenant hostname — Gateway 'hostname-hijack-probe' was created in tenant-test" >&2
    echo "$output" >&2
    return 1
  fi
}

@test "Package admission accepts gateway.attachedNamespaces with tenant-* entries" {
  # Regression guard: the previous shape rejected `tenant-*` entries in
  # publishing.gateway.attachedNamespaces (both a render-time fail in
  # cozystack-basics and a dedicated VAP). Under inheritance the attach
  # surface is governed by the namespace.cozystack.io/gateway label
  # selector, not by entries in attachedNamespaces — Layers 4/5/7
  # (Tenant.spec.host, namespace label, HTTPRoute hostname VAPs) defend
  # against hostname hijack independently. Adding a tenant-* entry must
  # no longer be blocked at admission. A future refactor that re-adds
  # the gate would silently break inheritance for every cluster that
  # has set attachedNamespaces explicitly.
  kubectl apply -f - <<'EOF'
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: vap-accept-probe
spec:
  variant: isp-full
  components:
    platform:
      values:
        gateway:
          attachedNamespaces:
          - tenant-alice
EOF
  # Apply succeeded → admission accepted the tenant-* entry. Clean up
  # immediately; this Package was just a probe and is not consumed by
  # the platform.
  kubectl delete package vap-accept-probe --ignore-not-found
}

@test "cozystack-tenant-host-policy blocks non-trusted callers from setting tenant.spec.host" {
  # Impersonate a tenant-scoped ServiceAccount that is NOT in the trustedCaller
  # group list. First grant RBAC to create Tenants — authorization runs BEFORE
  # admission, so without this grant the apiserver returns a plain RBAC
  # Forbidden and the test would fail grep-ing for 'ValidatingAdmissionPolicy'
  # even though the VAP itself is fine.
  kubectl apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: vap-probe-tenant-create
  namespace: tenant-test
rules:
- apiGroups: ["apps.cozystack.io"]
  resources: ["tenants"]
  verbs: ["create","get","list","watch","update","patch","delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: vap-probe-tenant-create
  namespace: tenant-test
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: vap-probe-tenant-create
subjects:
- kind: ServiceAccount
  name: default
  namespace: tenant-test
EOF
  if ! output=$(kubectl --as=system:serviceaccount:tenant-test:default \
                        --as-group=system:serviceaccounts \
                        --as-group=system:serviceaccounts:tenant-test \
    apply -f - 2>&1 <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: vaphostprobe
  namespace: tenant-test
spec:
  host: foreign.example.org
EOF
); then
    kubectl -n tenant-test delete rolebinding vap-probe-tenant-create --ignore-not-found
    kubectl -n tenant-test delete role vap-probe-tenant-create --ignore-not-found
    echo "$output" | grep -qi "ValidatingAdmissionPolicy"
    echo "$output" | grep -q "spec.host can only be set"
  else
    kubectl -n tenant-test delete tenants.apps.cozystack.io vaphostprobe --ignore-not-found
    kubectl -n tenant-test delete rolebinding vap-probe-tenant-create --ignore-not-found
    kubectl -n tenant-test delete role vap-probe-tenant-create --ignore-not-found
    echo "BUG: admission accepted tenant.spec.host from untrusted SA — Tenant 'vaphostprobe' was created" >&2
    echo "$output" >&2
    return 1
  fi
}

@test "cozystack-namespace-host-label-policy blocks non-trusted callers from changing the host label" {
  # tenant-test namespace already has namespace.cozystack.io/host set by the
  # cozystack tenant chart. Grant patch on namespaces cluster-wide to the
  # impersonated SA — namespaces is a cluster-scoped resource so this needs a
  # ClusterRole. Authorization runs before admission, so without this grant
  # the test would fail with plain RBAC Forbidden rather than a VAP rejection.
  kubectl apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: vap-probe-namespace-patch
rules:
- apiGroups: [""]
  resources: ["namespaces"]
  verbs: ["get","list","watch","update","patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: vap-probe-namespace-patch
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: vap-probe-namespace-patch
subjects:
- kind: ServiceAccount
  name: default
  namespace: tenant-test
EOF
  if ! output=$(kubectl --as=system:serviceaccount:tenant-test:default \
                        --as-group=system:serviceaccounts \
                        --as-group=system:serviceaccounts:tenant-test \
    label namespace tenant-test \
      namespace.cozystack.io/host=foreign.example.org --overwrite 2>&1); then
    kubectl delete clusterrolebinding vap-probe-namespace-patch --ignore-not-found
    kubectl delete clusterrole vap-probe-namespace-patch --ignore-not-found
    echo "$output" | grep -qi "ValidatingAdmissionPolicy"
    echo "$output" | grep -q "immutable"
  else
    # Revert label if apiserver somehow accepted the overwrite.
    kubectl label namespace tenant-test namespace.cozystack.io/host=test.example.org --overwrite
    kubectl delete clusterrolebinding vap-probe-namespace-patch --ignore-not-found
    kubectl delete clusterrole vap-probe-namespace-patch --ignore-not-found
    echo "BUG: admission accepted host label change from untrusted SA" >&2
    echo "$output" >&2
    return 1
  fi
}

@test "cozystack-namespace-host-label-policy blocks non-trusted callers from setting the host label at CREATE" {
  # Defense-in-depth: a non-trusted caller must not be able to stamp
  # namespace.cozystack.io/host=X on a brand-new namespace either — only
  # cozystack/Flux SAs may write the label. Authorization runs before
  # admission, so grant cluster-wide namespace create to the impersonated SA
  # first, otherwise the test would fail with plain RBAC Forbidden.
  kubectl apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: vap-probe-namespace-create
rules:
- apiGroups: [""]
  resources: ["namespaces"]
  verbs: ["create","get","list","delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: vap-probe-namespace-create
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: vap-probe-namespace-create
subjects:
- kind: ServiceAccount
  name: default
  namespace: tenant-test
EOF
  if ! output=$(kubectl --as=system:serviceaccount:tenant-test:default \
                        --as-group=system:serviceaccounts \
                        --as-group=system:serviceaccounts:tenant-test \
    apply -f - 2>&1 <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: vap-host-label-probe
  labels:
    namespace.cozystack.io/host: foreign.example.org
EOF
); then
    kubectl delete clusterrolebinding vap-probe-namespace-create --ignore-not-found
    kubectl delete clusterrole vap-probe-namespace-create --ignore-not-found
    echo "$output" | grep -qi "ValidatingAdmissionPolicy"
    echo "$output" | grep -q "immutable"
  else
    kubectl delete namespace vap-host-label-probe --ignore-not-found --wait=false
    kubectl delete clusterrolebinding vap-probe-namespace-create --ignore-not-found
    kubectl delete clusterrole vap-probe-namespace-create --ignore-not-found
    echo "BUG: admission accepted first-time host label write from untrusted SA at CREATE — Namespace 'vap-host-label-probe' was created" >&2
    echo "$output" >&2
    return 1
  fi
}

@test "TenantGateway CRD is installed (gateway.cozystack.io/v1alpha1)" {
  # Shipped via packages/system/cozystack-controller/definitions; the
  # controller cannot reconcile anything without it being Established.
  kubectl wait crd/tenantgateways.gateway.cozystack.io --for=condition=Established --timeout=60s
}

@test "TenantGatewayReconciler materialises Gateway + Issuer from a TenantGateway CR" {
  # Create the CR directly (the chart renders one of these per tenant
  # with tenant.spec.gateway=true; this test exercises the controller
  # in isolation without going through the full tenant flow).
  kubectl apply -f - <<'EOF'
apiVersion: gateway.cozystack.io/v1alpha1
kind: TenantGateway
metadata:
  name: tg-e2e-probe
  namespace: tenant-test
spec:
  apex: test.example.org
  certMode: http01
  gatewayClassName: cilium
EOF

  # Controller creates the Gateway (same name, same namespace).
  timeout 120 sh -ec 'until kubectl -n tenant-test get gateway tg-e2e-probe >/dev/null 2>&1; do sleep 2; done'
  kubectl -n tenant-test get gateway tg-e2e-probe -o yaml

  # And the per-tenant ACME Issuer.
  timeout 120 sh -ec 'until kubectl -n tenant-test get issuer.cert-manager.io tg-e2e-probe-gateway >/dev/null 2>&1; do sleep 2; done'

  # Status is reported back: ObservedGeneration tracks .metadata.generation,
  # Ready=True after a clean reconcile.
  timeout 120 sh -ec 'until kubectl -n tenant-test get tenantgateways.gateway.cozystack.io tg-e2e-probe -o jsonpath="{.status.conditions[?(@.type==\"Ready\")].status}" 2>/dev/null | grep -q True; do sleep 2; done'

  # Cleanup. Cascade-delete relies on OwnerReferences set by the reconciler.
  kubectl -n tenant-test delete tenantgateways.gateway.cozystack.io tg-e2e-probe --ignore-not-found --timeout=1m
}

@test "cozystack-route-hostname-policy VAP rejects HTTPRoute claiming a foreign apex" {
  # tenant-test's namespace.cozystack.io/host label is test.example.org.
  # An HTTPRoute claiming attacker.com (or any hostname not under
  # test.example.org) must be denied by Layer 7 of the security model.
  if ! output=$(kubectl apply -f - 2>&1 <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: route-hostname-hijack-probe
  namespace: tenant-test
spec:
  hostnames:
  - "attacker.com"
  rules:
  - backendRefs:
    - name: kubernetes
      namespace: default
      port: 443
EOF
); then
    echo "$output" | grep -qi "ValidatingAdmissionPolicy"
    echo "$output" | grep -qi "hostnames must equal"
  else
    kubectl -n tenant-test delete httproute route-hostname-hijack-probe --ignore-not-found
    echo "BUG: admission accepted cross-apex HTTPRoute hostname — route was created" >&2
    echo "$output" >&2
    return 1
  fi
}

@test "cozystack-route-hostname-policy VAP allows HTTPRoute under the namespace's apex" {
  # Sanity check the inverse: a route with a hostname under
  # test.example.org must NOT be rejected by the VAP.
  kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: route-hostname-allow-probe
  namespace: tenant-test
spec:
  hostnames:
  - "harbor.test.example.org"
  rules:
  - backendRefs:
    - name: kubernetes
      namespace: default
      port: 443
EOF

  kubectl -n tenant-test delete httproute route-hostname-allow-probe --ignore-not-found
}

@test "HTTPRoute with a matching parentRef reaches Accepted status" {
  # Put a Gateway and a route in the same namespace so allowedRoutes: Same accepts them.
  kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gateway-route-probe
  namespace: tenant-test
spec:
  gatewayClassName: cilium
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    allowedRoutes:
      namespaces:
        from: Same
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: httproute-probe
  namespace: tenant-test
spec:
  parentRefs:
  - name: gateway-route-probe
    sectionName: http
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /
    backendRefs:
    - name: kubernetes
      namespace: default
      port: 443
EOF

  kubectl -n tenant-test wait gateway/gateway-route-probe --for=condition=Programmed --timeout=3m
  timeout 120 sh -ec 'until kubectl -n tenant-test get httproute httproute-probe -o jsonpath="{.status.parents[0].conditions[?(@.type==\"Accepted\")].status}" 2>/dev/null | grep -q True; do sleep 2; done'

  kubectl -n tenant-test delete httproute/httproute-probe --ignore-not-found
  kubectl -n tenant-test delete gateway/gateway-route-probe --ignore-not-found
}

@test "tenant chart writes namespace.cozystack.io/gateway label and _namespace.gateway in lockstep with tenant.gatewayEffective" {
  # Pins the contract that namespace.yaml, gateway.yaml, and any other
  # tenant-chart consumer of the gateway flag resolve through the same
  # tenant.gatewayEffective helper. If they diverge — opt-in Tenant
  # rendering Gateway HelmRelease but namespace.yaml leaving
  # _namespace.gateway empty — child apps (harbor, bucket) silently
  # fall back to Ingress while the Gateway resource sits unused.
  #
  # Uses explicit opt-in (tenant.spec.gateway=true) so the test is
  # self-contained and does not require platform-level
  # gateway.enabled=true in the e2e setup. The derived-apex
  # auto-default path through _cluster.gateway-enabled is covered by
  # helm-unittest fixtures in packages/apps/tenant/tests/
  # gateway_default_test.yaml; both paths converge in the same helper,
  # so e2e only needs to exercise one to guard the lockstep contract.
  #
  # Provisions a child tenant under tenant-test with explicit
  # gateway=true and asserts:
  # - the child Namespace carries namespace.cozystack.io/gateway=<self>
  # - the cozystack-values Secret in the child namespace has
  #   _namespace.gateway: "<self>" matching tenant.gatewayEffective
  kubectl apply -f - <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: gwprop
  namespace: tenant-test
spec:
  gateway: true
EOF
  timeout 180 sh -ec 'until kubectl get ns tenant-test-gwprop >/dev/null 2>&1; do sleep 3; done'
  timeout 60 sh -ec 'until kubectl -n tenant-test-gwprop get secret cozystack-values >/dev/null 2>&1; do sleep 2; done'

  ns_label=$(kubectl get ns tenant-test-gwprop -o jsonpath='{.metadata.labels.namespace\.cozystack\.io/gateway}')
  [ "$ns_label" = "tenant-test-gwprop" ]

  cozyvalues=$(kubectl -n tenant-test-gwprop get secret cozystack-values -o jsonpath='{.data.values\.yaml}' | base64 -d)
  echo "$cozyvalues" | grep -E '^\s*gateway:\s*"tenant-test-gwprop"\s*$' >/dev/null

  kubectl -n tenant-test delete tenant gwprop --ignore-not-found
}

@test "child tenant without explicit gateway inherits _namespace.gateway from a Gateway-owning parent" {
  # Pins the inheritance flow: when a parent tenant owns its Gateway
  # (gateway=true), a child tenant under that parent with the gateway
  # field unset receives _namespace.gateway = <parent-tenant-name> in
  # its cozystack-values Secret AND the same value on the
  # namespace.cozystack.io/gateway label of the child namespace.
  #
  # Without this lockstep, the parent's Gateway label-selector
  # allowedRoutes does not match the child namespace at runtime, and
  # the child's HTTPRoutes silently fail to attach.
  #
  # tenant-test in the e2e fixture has no Gateway of its own, so the
  # test sets up its own parent (gwparent) under tenant-test with
  # explicit gateway=true. The child (gwchild) under that parent is
  # the inheritance-under-test.
  kubectl apply -f - <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: gwparent
  namespace: tenant-test
spec:
  gateway: true
EOF
  timeout 180 sh -ec 'until kubectl get ns tenant-test-gwparent >/dev/null 2>&1; do sleep 3; done'
  timeout 60 sh -ec 'until kubectl -n tenant-test-gwparent get secret cozystack-values >/dev/null 2>&1; do sleep 2; done'
  parent_label=$(kubectl get ns tenant-test-gwparent -o jsonpath='{.metadata.labels.namespace\.cozystack\.io/gateway}')
  [ "$parent_label" = "tenant-test-gwparent" ]

  kubectl apply -f - <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: gwchild
  namespace: tenant-test-gwparent
spec: {}
EOF
  timeout 180 sh -ec 'until kubectl get ns tenant-test-gwparent-gwchild >/dev/null 2>&1; do sleep 3; done'
  timeout 60 sh -ec 'until kubectl -n tenant-test-gwparent-gwchild get secret cozystack-values >/dev/null 2>&1; do sleep 2; done'

  child_label=$(kubectl get ns tenant-test-gwparent-gwchild -o jsonpath='{.metadata.labels.namespace\.cozystack\.io/gateway}')
  [ "$child_label" = "tenant-test-gwparent" ]

  cozyvalues=$(kubectl -n tenant-test-gwparent-gwchild get secret cozystack-values -o jsonpath='{.data.values\.yaml}' | base64 -d)
  echo "$cozyvalues" | grep -E '^\s*gateway:\s*"tenant-test-gwparent"\s*$' >/dev/null

  # The child must NOT have its own gateway HelmRelease — inheritance
  # means no separate Gateway resource for the child tenant.
  ! kubectl -n tenant-test-gwparent-gwchild get helmrelease gateway 2>/dev/null

  kubectl -n tenant-test-gwparent delete tenant gwchild --ignore-not-found
  kubectl -n tenant-test delete tenant gwparent --ignore-not-found
}

@test "child tenant's HTTPRoute drives the parent Gateway's listener set via inheritance label" {
  # End-to-end cross-namespace attach for the inheritance flow:
  # parent owns the Gateway, child inherits via the
  # namespace.cozystack.io/gateway label, and an HTTPRoute in the
  # child namespace causes the parent Gateway to grow a per-listener
  # HTTPS entry for the route's hostname plus a per-listener
  # Certificate object. Both writes are cozystack-controller's
  # responsibility — they happen only if collectHostnameClaims sees
  # the route across the inheritance label.
  #
  # The Cilium-side `HTTPRoute.status.parents[].conditions[Accepted]`
  # flip is NOT checked here. In the e2e cluster the ACME server
  # (LE prod) refuses to issue any certificate for `.example.org`
  # (forbidden by policy), so a fresh per-listener cert never goes
  # Ready, which Cilium ties to listener readiness, which blocks
  # Accepted on the route. Every cluster cert visible in cozyreport
  # (alerta, grafana, dashboard, seaweedfs-s3) shows the same
  # `rejectedIdentifier` for the same reason — bootstrap-time certs
  # survive on stale issuance, fresh ones can't be issued. Asserting
  # Accepted=True against that environment is environmental noise,
  # not a contract on the inheritance code path.
  #
  # The route's hostname is constructed under the child's derived
  # apex (<child-name>.<parent-apex>) so Layer 7
  # (cozystack-route-hostname-policy) admits it.
  kubectl apply -f - <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: rparent
  namespace: tenant-test
spec:
  gateway: true
EOF
  timeout 180 sh -ec 'until kubectl get ns tenant-test-rparent >/dev/null 2>&1; do sleep 3; done'
  # Wait for the cozystack-controller to materialise the parent's
  # Gateway resource. Programmed status requires Cilium dataplane
  # provisioning which depends on the LB allocator; for this test
  # we only need the Gateway object to exist + carry the
  # label-selector allowedRoutes.
  timeout 120 sh -ec 'until kubectl -n tenant-test-rparent get gateway cozystack >/dev/null 2>&1; do sleep 3; done'

  parent_apex=$(kubectl get ns tenant-test-rparent -o jsonpath='{.metadata.labels.namespace\.cozystack\.io/host}')
  [ -n "$parent_apex" ]

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: rchild
  namespace: tenant-test-rparent
spec: {}
EOF
  timeout 180 sh -ec 'until kubectl get ns tenant-test-rparent-rchild >/dev/null 2>&1; do sleep 3; done'

  # Verify the inheritance label propagated to the child namespace —
  # this is the read side of the contract collectHostnameClaims
  # exercises.
  child_gateway_label=$(kubectl get ns tenant-test-rparent-rchild -o jsonpath='{.metadata.labels.namespace\.cozystack\.io/gateway}')
  [ "$child_gateway_label" = "tenant-test-rparent" ]

  child_apex=$(kubectl get ns tenant-test-rparent-rchild -o jsonpath='{.metadata.labels.namespace\.cozystack\.io/host}')
  [ -n "$child_apex" ]

  route_host="harbor.${child_apex}"

  kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: inherit-probe
  namespace: tenant-test-rparent-rchild
spec:
  parentRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: cozystack
    namespace: tenant-test-rparent
  hostnames:
  - "${route_host}"
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /
    backendRefs:
    - name: kubernetes
      namespace: default
      port: 443
EOF

  # cozystack-controller's watch on HTTPRoute fires, collectHostnameClaims
  # picks up the route through the inheritance label (the contract this
  # test pins), reconcileGateway adds the per-listener HTTPS entry for
  # the child route's hostname.
  timeout 120 sh -ec '
    want="'"$route_host"'"
    until kubectl -n tenant-test-rparent get gateway cozystack \
      -o jsonpath="{range .spec.listeners[?(@.protocol==\"HTTPS\")]}{.hostname}{\"\n\"}{end}" 2>/dev/null \
      | grep -qx "$want"; do
      sleep 3
    done
  '

  # reconcilePerListenerCertificates runs from the same dynHostnames
  # slice — a missing Certificate here would mean the controller
  # rendered the listener but not its cert ref, which would silently
  # leak listeners with broken TLS refs.
  timeout 60 sh -ec '
    want="'"$route_host"'"
    until kubectl -n tenant-test-rparent get certificate \
      -l cozystack.io/per-listener-cert=true \
      -o jsonpath="{range .items[*]}{.spec.dnsNames[0]}{\"\n\"}{end}" 2>/dev/null \
      | grep -qx "$want"; do
      sleep 3
    done
  '

  kubectl -n tenant-test-rparent-rchild delete httproute inherit-probe --ignore-not-found
  kubectl -n tenant-test-rparent delete tenant rchild --ignore-not-found
  kubectl -n tenant-test delete tenant rparent --ignore-not-found
}
