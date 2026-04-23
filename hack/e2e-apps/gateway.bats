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

@test "exposed services render HTTPRoute/TLSRoute but not Ingress when gateway.enabled=true" {
  # With gateway.enabled=true in the install bundle, dashboard and keycloak
  # must render HTTPRoute; cozystack-api, vm-exportproxy and cdi-uploadproxy
  # must render TLSRoute; and none of them should have a legacy Ingress.
  kubectl -n cozy-dashboard wait httproute/dashboard --for=condition=Accepted --timeout=2m
  kubectl -n cozy-keycloak wait httproute/keycloak --for=condition=Accepted --timeout=2m

  kubectl -n default get tlsroute kubernetes-api
  kubectl -n cozy-kubevirt get tlsroute vm-exportproxy
  kubectl -n cozy-kubevirt-cdi get tlsroute cdi-uploadproxy

  # The old Ingress objects for these services must be absent. If they're
  # still around, the 'gateway.enabled gate' did not exclude them on render.
  ! kubectl -n cozy-dashboard get ingress dashboard-web-ingress 2>/dev/null
  ! kubectl -n cozy-keycloak get ingress keycloak-ingress 2>/dev/null
  ! kubectl -n default get ingress kubernetes 2>/dev/null
  ! kubectl -n cozy-kubevirt get ingress vm-exportproxy 2>/dev/null
  ! kubectl -n cozy-kubevirt-cdi get ingress cdi-uploadproxy 2>/dev/null
}

@test "ValidatingAdmissionPolicy rejects Gateway with foreign hostname" {
  # tenant-test namespace should only be allowed to publish its own
  # domain suffix ('.test.example.org'); a listener hostname from the
  # root tenant's apex must be denied by cozystack-gateway-hostname-policy.
  run kubectl apply -f - <<'EOF'
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
  # Expect kubectl to fail with an admission error
  [ "$status" -ne 0 ]
  echo "$output" | grep -qi "ValidatingAdmissionPolicy"
  echo "$output" | grep -q "must equal test.example.org"
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
