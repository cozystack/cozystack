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
  # hack/cozytest.sh is a pure-shell bats-compat runner — bats' `run` helper
  # is NOT available, so we capture kubectl output and exit status manually.
  output=$(kubectl apply -f - 2>&1 <<'EOF' && echo "__SUCCEEDED__"
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
)
  # Expect kubectl to fail with an admission error (no __SUCCEEDED__ marker).
  echo "$output" | grep -q "__SUCCEEDED__" && { echo "BUG: admission accepted cross-tenant hostname" >&2; return 1; }
  echo "$output" | grep -qi "ValidatingAdmissionPolicy"
  echo "$output" | grep -q "must equal test.example.org"
}

@test "cozystack-gateway-attached-namespaces-policy rejects Packages with tenant-* entries" {
  # The platform Package default name is cozystack.cozystack-platform, managed by
  # cozystack-api. Creating a dummy Package with tenant-alice in gateway.attachedNamespaces
  # must fail at admission time.
  output=$(kubectl apply -f - 2>&1 <<'EOF' && echo "__SUCCEEDED__"
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: vap-reject-probe
spec:
  variant: isp-full
  components:
    platform:
      values:
        gateway:
          attachedNamespaces:
          - tenant-alice
EOF
)
  echo "$output" | grep -q "__SUCCEEDED__" && { echo "BUG: admission accepted tenant-* in attachedNamespaces" >&2; return 1; }
  echo "$output" | grep -qi "ValidatingAdmissionPolicy"
  echo "$output" | grep -q "must not contain any tenant-"
}

@test "cozystack-tenant-host-policy blocks non-trusted callers from setting tenant.spec.host" {
  # Impersonate a tenant-scoped ServiceAccount that is NOT in the trustedCaller
  # group list. Attempt to create a Tenant with spec.host set → rejected.
  output=$(kubectl --as=system:serviceaccount:tenant-test:default \
                   --as-group=system:serviceaccounts \
                   --as-group=system:serviceaccounts:tenant-test \
    apply -f - 2>&1 <<'EOF' && echo "__SUCCEEDED__"
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: vap-host-probe
  namespace: tenant-test
spec:
  host: foreign.example.org
EOF
)
  echo "$output" | grep -q "__SUCCEEDED__" && { echo "BUG: admission accepted tenant.spec.host from untrusted SA" >&2; return 1; }
  echo "$output" | grep -qi "ValidatingAdmissionPolicy"
  echo "$output" | grep -q "spec.host can only be set"
}

@test "cozystack-namespace-host-label-policy blocks non-trusted callers from changing the host label" {
  # tenant-test namespace already has namespace.cozystack.io/host set by the
  # cozystack tenant chart. An unprivileged SA must not be able to overwrite it.
  output=$(kubectl --as=system:serviceaccount:tenant-test:default \
                   --as-group=system:serviceaccounts \
                   --as-group=system:serviceaccounts:tenant-test \
    label namespace tenant-test \
      namespace.cozystack.io/host=foreign.example.org --overwrite 2>&1 && echo "__SUCCEEDED__")
  echo "$output" | grep -q "__SUCCEEDED__" && { echo "BUG: admission accepted host label change from untrusted SA" >&2; return 1; }
  echo "$output" | grep -qi "ValidatingAdmissionPolicy"
  echo "$output" | grep -q "immutable"
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
