#!/usr/bin/env bats

# Exercises the SecurityGroup membership model against a live cluster. Two
# things unit tests cannot prove: that the in-tree CiliumNetworkPolicy mirror
# (membership endpointSelector, app/SG peer selectors, attachments annotation)
# round-trips against the real cilium.io/v2 CRD, and that the
# securitygroup-controller actually stamps the membership label onto an attached
# application's pods.

@test "SecurityGroup projects onto a marked CiliumNetworkPolicy and back" {
  ns='tenant-test'
  name='sg-e2e'
  peer='sg-frontend-e2e'
  plain='plain-cnp-e2e'

  kubectl -n "$ns" delete securitygroup.sdn.cozystack.io "$name" --ignore-not-found --timeout=1m
  kubectl -n "$ns" delete ciliumnetworkpolicy "$plain" --ignore-not-found --timeout=1m

  # Create a SecurityGroup through the aggregated API. It attaches to a managed
  # application; the backing endpointSelector is the group's own membership
  # label, and peers are expressed as fromApp/fromSG.
  kubectl apply -f- <<EOF
apiVersion: sdn.cozystack.io/v1alpha1
kind: SecurityGroup
metadata:
  name: $name
  namespace: $ns
spec:
  attachments:
    - kind: Postgres
      name: db
  ingress:
    - fromApp:
        - kind: Kubernetes
          name: web
      fromSG:
        - $peer
      toPorts:
        - ports:
            - port: "5432"
              protocol: TCP
EOF

  # The backing CiliumNetworkPolicy must materialise with the marker label.
  timeout 60 sh -ec "until kubectl -n $ns get ciliumnetworkpolicy $name >/dev/null 2>&1; do sleep 2; done"

  marker=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.metadata.labels.sdn\.cozystack\.io/securitygroup}')
  [ "$marker" = "true" ]

  # The endpointSelector must be the group's own membership label — the dotted
  # key must serialise and be accepted by the real cilium.io/v2 CRD. The empty
  # value must round-trip (jsonpath cannot tell empty from absent, so assert the
  # key exists via jq).
  kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o json \
    | jq -e ".spec.endpointSelector.matchLabels | has(\"securitygroup.sdn.cozystack.io/$name\")" >/dev/null

  # The attachments live in a storage-owned annotation on the backing policy.
  att=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.metadata.annotations.sdn\.cozystack\.io/attachments}')
  echo "$att" | jq -e '.[0].kind == "Postgres" and .[0].name == "db"' >/dev/null

  # fromApp projects to lineage-label fromEndpoints; fromSG to the peer group's
  # membership-label fromEndpoints. Assert all three lineage labels (group too,
  # so a regression dropping the group key is caught).
  selGroup=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.spec.ingress[0].fromEndpoints[0].matchLabels.apps\.cozystack\.io/application\.group}')
  [ "$selGroup" = "apps.cozystack.io" ]
  selKind=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.spec.ingress[0].fromEndpoints[0].matchLabels.apps\.cozystack\.io/application\.kind}')
  [ "$selKind" = "Kubernetes" ]
  selName=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.spec.ingress[0].fromEndpoints[0].matchLabels.apps\.cozystack\.io/application\.name}')
  [ "$selName" = "web" ]
  kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o json \
    | jq -e ".spec.ingress[0].fromEndpoints[1].matchLabels | has(\"securitygroup.sdn.cozystack.io/$peer\")" >/dev/null

  # The rule list must be translated 1:1 (port carried through, protocol upper-cased).
  port=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.spec.ingress[0].toPorts[0].ports[0].port}')
  [ "$port" = "5432" ]
  proto=$(kubectl -n "$ns" get ciliumnetworkpolicy "$name" -o jsonpath='{.spec.ingress[0].toPorts[0].ports[0].protocol}')
  [ "$proto" = "TCP" ]

  # The SecurityGroup view reconstructs attachments and peers, round-trips, and
  # hides the internal marker label and attachments annotation.
  kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$name"
  attKind=$(kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$name" -o jsonpath='{.spec.attachments[0].kind}')
  [ "$attKind" = "Postgres" ]
  fromApp=$(kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$name" -o jsonpath='{.spec.ingress[0].fromApp[0].name}')
  [ "$fromApp" = "web" ]
  fromSG=$(kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$name" -o jsonpath='{.spec.ingress[0].fromSG[0]}')
  [ "$fromSG" = "$peer" ]
  viewMarker=$(kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$name" -o jsonpath='{.metadata.labels.sdn\.cozystack\.io/securitygroup}')
  [ -z "$viewMarker" ]
  viewAtt=$(kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$name" -o jsonpath='{.metadata.annotations.sdn\.cozystack\.io/attachments}')
  [ -z "$viewAtt" ]

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
  # A rule is mandatory: the CiliumNetworkPolicy CRD requires at least one of
  # ingress/ingressDeny/egress/egressDeny (spec anyOf), so a selector-only
  # policy is rejected at apply time. This keeps the fixture a valid, applyable
  # CNP while leaving it unmarked (no securitygroup label) — so it must stay
  # invisible to the SecurityGroup API.
  ingress:
    - fromEndpoints:
        - matchLabels:
            app: peer
EOF
  ! kubectl -n "$ns" get securitygroup.sdn.cozystack.io "$plain" 2>/dev/null

  # Deleting the SecurityGroup removes its backing policy; the unmarked one stays.
  kubectl -n "$ns" delete securitygroup.sdn.cozystack.io "$name" --timeout=1m
  timeout 60 sh -ec "while kubectl -n $ns get ciliumnetworkpolicy $name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n "$ns" get ciliumnetworkpolicy "$plain"

  # Cleanup.
  kubectl -n "$ns" delete ciliumnetworkpolicy "$plain" --ignore-not-found --timeout=1m
}

@test "securitygroup-controller stamps and clears the membership label" {
  ns='tenant-test'
  name='sg-member-e2e'
  app='sg-member-app'
  podname='sg-member-pod-e2e'
  key="securitygroup.sdn.cozystack.io/$name"

  kubectl -n "$ns" delete securitygroup.sdn.cozystack.io "$name" --ignore-not-found --timeout=1m
  kubectl -n "$ns" delete pod "$podname" --ignore-not-found --timeout=1m

  # A pod carrying the lineage labels of a managed application. The lineage
  # webhook skips it (managed-by is already set), so these labels are exactly
  # what the controller resolves the attachment to.
  kubectl apply -f- <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $podname
  namespace: $ns
  labels:
    internal.cozystack.io/managed-by-cozystack: "true"
    apps.cozystack.io/application.group: apps.cozystack.io
    apps.cozystack.io/application.kind: Postgres
    apps.cozystack.io/application.name: $app
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: ["ALL"]
EOF
  timeout 60 sh -ec "until kubectl -n $ns get pod $podname >/dev/null 2>&1; do sleep 2; done"

  # Attach a SecurityGroup to that application.
  kubectl apply -f- <<EOF
apiVersion: sdn.cozystack.io/v1alpha1
kind: SecurityGroup
metadata:
  name: $name
  namespace: $ns
spec:
  attachments:
    - kind: Postgres
      name: $app
EOF

  # The controller must stamp the membership label onto the member pod.
  timeout 120 sh -ec "until kubectl -n $ns get pods -l '$key' -o name 2>/dev/null | grep -q 'pod/$podname'; do sleep 3; done" \
    || { echo "membership label $key never appeared on $podname"; kubectl -n "$ns" get pod "$podname" -o yaml; kubectl -n cozy-securitygroup-controller get pods; false; }

  # Deleting the SecurityGroup while a pod is still a member must strip the
  # membership label off that pod (the controller's finalizer runs before the
  # backing policy is garbage-collected), leaving no orphaned label. This is the
  # delete-with-members path, distinct from detach.
  kubectl -n "$ns" delete securitygroup.sdn.cozystack.io "$name" --timeout=2m
  timeout 60 sh -ec "while kubectl -n $ns get ciliumnetworkpolicy $name >/dev/null 2>&1; do sleep 2; done"
  timeout 60 sh -ec "until ! kubectl -n $ns get pods -l '$key' -o name 2>/dev/null | grep -q 'pod/$podname'; do sleep 2; done" \
    || { echo "membership label $key orphaned on $podname after SecurityGroup delete"; kubectl -n "$ns" get pod "$podname" -o yaml; false; }

  # Cleanup.
  kubectl -n "$ns" delete pod "$podname" --ignore-not-found --timeout=1m
}
