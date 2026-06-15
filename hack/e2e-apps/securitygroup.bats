#!/usr/bin/env bats

# Exercises the SecurityGroup aggregated API against a live cluster: the one
# contract unit tests cannot prove is that the in-tree CiliumNetworkPolicy
# mirror round-trips against the real cilium.io/v2 CRD.

@test "SecurityGroup projects onto a marked CiliumNetworkPolicy and back" {
  ns='tenant-test'
  name='sg-e2e'
  plain='plain-cnp-e2e'

  kubectl -n "$ns" delete securitygroup.sdn.cozystack.io "$name" --ignore-not-found --timeout=1m || true
  kubectl -n "$ns" delete ciliumnetworkpolicy "$plain" --ignore-not-found --timeout=1m || true

  # Create a SecurityGroup through the aggregated API.
  kubectl apply -f- <<EOF
apiVersion: sdn.cozystack.io/v1alpha1
kind: SecurityGroup
metadata:
  name: $name
  namespace: $ns
spec:
  endpointSelector:
    matchLabels:
      app: $name
  ingress:
    - fromEndpoints:
        - matchLabels:
            app: client
      toPorts:
        - ports:
            - port: "5432"
              protocol: TCP
EOF

  # The backing CiliumNetworkPolicy must materialise with the marker label.
  timeout 60 sh -ec "until kubectl -n $ns get ciliumnetworkpolicy $name >/dev/null 2>&1; do sleep 2; done"

  marker=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.metadata.labels.sdn\.cozystack\.io/securitygroup}')
  [ "$marker" = "true" ]

  # The spec must be translated 1:1 (port carried through, protocol upper-cased).
  port=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.spec.ingress[0].toPorts[0].ports[0].port}')
  [ "$port" = "5432" ]
  proto=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.spec.ingress[0].toPorts[0].ports[0].protocol}')
  [ "$proto" = "TCP" ]

  # The SecurityGroup view round-trips and hides the internal marker label.
  kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$name"
  viewMarker=$(kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$name" -o jsonpath='{.metadata.labels.sdn\.cozystack\.io/securitygroup}')
  [ -z "$viewMarker" ]

  # An unmarked CiliumNetworkPolicy must stay invisible to the SecurityGroup API.
  kubectl apply -f- <<EOF
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: $plain
  namespace: $ns
spec:
  endpointSelector:
    matchLabels:
      app: untouched
EOF
  ! kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$plain" 2>/dev/null

  # Deleting the SecurityGroup removes its backing policy; the unmarked one stays.
  kubectl -n "$ns" delete securitygroup.sdn.cozystack.io "$name" --timeout=1m
  ! kubectl -n "$ns" get ciliumnetworkpolicy "$name" 2>/dev/null
  kubectl -n "$ns" get ciliumnetworkpolicy "$plain"

  # Cleanup.
  kubectl -n "$ns" delete ciliumnetworkpolicy "$plain" --ignore-not-found --timeout=1m || true
}
