#!/usr/bin/env bats

# Smoke test for the ServiceExposure controller (network.cozystack.io).
#
# This exercises the externalIPs backend end-to-end on a live cluster: the
# CRDs are installed, the controller resolves a ServiceExposure to its
# ExposureClass, renders NO foreign backend CRs for the externalIPs
# backend, and reports status. The externalIPs path is chosen deliberately
# so the test creates no MetalLB/Cilium pool — it cannot collide with the
# cluster's existing LoadBalancer address pools or depend on real L2
# reachability, keeping it deterministic. The metallb and cilium rendering
# paths (pool + announcer CRs, garbage collection, take-over guard) are
# covered exhaustively by the Go unit tests in
# internal/controller/serviceexposure.

CLASS=se-e2e-externalips
SVC=se-e2e-svc
EXP=se-e2e
POOL=cozystack-${CLASS} # backend.PoolName = cozystack-<class> (class-level)

setup() {
  kubectl -n tenant-test delete serviceexposure "$EXP" --ignore-not-found --timeout=2m
  kubectl -n tenant-test wait serviceexposure/"$EXP" --for=delete --timeout=2m
  kubectl -n tenant-test delete service "$SVC" --ignore-not-found --timeout=1m
  kubectl delete exposureclass "$CLASS" --ignore-not-found --timeout=1m
}

# teardown runs after every test, so a test that fails mid-way does not
# leave the cluster-scoped ExposureClass (or the namespaced fixtures)
# behind for the next test or the next CI run.
teardown() {
  kubectl -n tenant-test delete serviceexposure "$EXP" --ignore-not-found --timeout=2m
  kubectl -n tenant-test delete service "$SVC" --ignore-not-found --timeout=1m
  kubectl delete exposureclass "$CLASS" --ignore-not-found --timeout=1m
}

dump_diagnostics() {
  echo "# --- diagnostics ---" >&3
  kubectl -n tenant-test get serviceexposure,service -o wide >&3 2>&1 || true
  kubectl -n tenant-test describe serviceexposure "$EXP" >&3 2>&1 || true
  kubectl get exposureclass "$CLASS" -o yaml >&3 2>&1 || true
  kubectl -n cozy-system logs -l app=cozystack-controller --tail=100 >&3 2>&1 || true
}

@test "externalIPs ExposureClass resolves and renders no backend pool" {
  kubectl apply -f- <<EOF
apiVersion: network.cozystack.io/v1alpha1
kind: ExposureClass
metadata:
  name: ${CLASS}
spec:
  backend: externalIPs
---
apiVersion: v1
kind: Service
metadata:
  name: ${SVC}
  namespace: tenant-test
spec:
  type: ClusterIP
  externalIPs:
    - 203.0.113.10
  ports:
    - port: 80
      targetPort: 80
---
apiVersion: network.cozystack.io/v1alpha1
kind: ServiceExposure
metadata:
  name: ${EXP}
  namespace: tenant-test
spec:
  serviceRef:
    name: ${SVC}
  exposureClassName: ${CLASS}
EOF

  # The controller stamps status.resolvedBackend once it has reconciled.
  timeout 120 sh -ec "until [ \"\$(kubectl -n tenant-test get serviceexposure ${EXP} -o jsonpath='{.status.resolvedBackend}' 2>/dev/null)\" = 'externalIPs' ]; do sleep 2; done" || { dump_diagnostics; false; }

  # externalIPs backend must not create a MetalLB pool.
  ! kubectl -n cozy-metallb get ipaddresspool "$POOL" 2>/dev/null || { dump_diagnostics; false; }

  # The pinned externalIPs make the exposure Ready.
  kubectl -n tenant-test wait serviceexposure/"$EXP" --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True --timeout=60s || { dump_diagnostics; false; }
  kubectl -n tenant-test get serviceexposure "$EXP" -o jsonpath='{.status.assignedIPs[0]}' | grep -q '^203.0.113.10$' || { dump_diagnostics; false; }
}

@test "deleting the ServiceExposure removes its finalizer and the object" {
  # Recreate (setup cleared the previous test's objects).
  kubectl apply -f- <<EOF
apiVersion: network.cozystack.io/v1alpha1
kind: ExposureClass
metadata:
  name: ${CLASS}
spec:
  backend: externalIPs
---
apiVersion: v1
kind: Service
metadata:
  name: ${SVC}
  namespace: tenant-test
spec:
  type: ClusterIP
  externalIPs:
    - 203.0.113.10
  ports:
    - port: 80
      targetPort: 80
---
apiVersion: network.cozystack.io/v1alpha1
kind: ServiceExposure
metadata:
  name: ${EXP}
  namespace: tenant-test
spec:
  serviceRef:
    name: ${SVC}
  exposureClassName: ${CLASS}
EOF
  timeout 120 sh -ec "until kubectl -n tenant-test get serviceexposure ${EXP} -o jsonpath='{.status.resolvedBackend}' 2>/dev/null | grep -q externalIPs; do sleep 2; done" || { dump_diagnostics; false; }

  # Deletion must complete despite the cleanup finalizer (nothing to GC for
  # the externalIPs backend, so the finalizer is removed promptly).
  kubectl -n tenant-test delete serviceexposure "$EXP" --timeout=2m
  kubectl -n tenant-test wait serviceexposure/"$EXP" --for=delete --timeout=1m

  # Inline cleanup of the remaining fixtures.
  kubectl -n tenant-test delete service "$SVC" --ignore-not-found --timeout=1m
  kubectl delete exposureclass "$CLASS" --ignore-not-found --timeout=1m
}
