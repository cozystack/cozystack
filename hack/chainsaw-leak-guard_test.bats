#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the leak guard: hack/e2e-chainsaw/_lib/leak-guard.sh and the
# per-suite runner hack/e2e-chainsaw-run.sh that calls it.
#
# What these pin is the attribution rule (which leftover pod or cert-manager
# Secret is charged to the suite that just ran, and which is left alone), the
# budget (a pod still tearing down inside it passes, one past it fails), the
# allowlist (an exemption warns instead of failing, and cannot outlive its suite
# directory), the JUnit merge, and that the runner hands chainsaw the same
# suites a single invocation would.
#
# kubectl and chainsaw are stubs on PATH that answer from fixture files, so the
# cluster is a directory: pods.json is what `get pods` returns (pods.after.json
# once a `kubectl wait` has succeeded), secrets.json and secrets.after.json the
# same for `get secrets`, which answers an empty list when there is neither,
# obj/ holds the owners the walk may look up, and `now` is the apiserver clock.
# The guard is bash; the legacy
# cozytest.sh translator runs these bodies under sh, so each test calls it
# through `bash`.
#
# CI runs this file under Bats. It stays compatible with that translator, whose
# awk parser recognizes only @test blocks and a bare `}` on its own line, so the
# tests avoid bats `run`, `$status`, setup() and teardown(). Each test removes
# its own temp dir on its last line. The parser also puts `return 0` in front of
# every bare `}` in the file, top-level helpers included, so a helper whose exit
# status matters closes with `} # name`.
#
# The file name carries no `e2e-` prefix on purpose: the root Makefile leaves
# hack/e2e-*.bats out of `make unit-tests`, and cozytest.sh arms its cluster
# captures for them.
#
# Run with: bats hack/chainsaw-leak-guard_test.bats
# -----------------------------------------------------------------------------

load test_helper

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
LG_LIB="$HACK_DIR/e2e-chainsaw/_lib/leak-guard.sh"
LG_RUN="$HACK_DIR/e2e-chainsaw-run.sh"
export LG_LIB

# The suite under test started here, on the apiserver's clock.
T_START=2026-09-24T15:10:00Z
T_OLD=2026-09-24T14:00:00Z

# lg FUNCTION ARGS... — call a guard function in bash with the stubs on PATH.
lg() {
  PATH="$FIX/bin:$PATH" bash -c '. "$LG_LIB"; "$@"' lg "$@"
} # lg

# new_fixture — a fresh fake cluster in $FIX, with stub kubectl and chainsaw.
new_fixture() {
  FIX=$(mktemp -d)
  export FIX
  mkdir -p "$FIX/bin" "$FIX/obj"
  printf '%s' "$T_START" >"$FIX/now"
  cat >"$FIX/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FIX/kubectl-calls"
case "$1 $2" in
  "create configmap") cat "$FIX/now"; exit ;;
  "get pods")
    # pods-failures: how many pod lists fail once a wait has run.
    n=$(cat "$FIX/pods-failures" 2>/dev/null || echo 0)
    if [ -f "$FIX/wait-called" ] && [ "$n" -gt 0 ]; then
      echo $((n - 1)) >"$FIX/pods-failures"
      echo 'Unable to connect to the server: net/http: TLS handshake timeout' >&2
      exit 1
    fi
    if [ -f "$FIX/waited" ] && [ -f "$FIX/pods.after.json" ]; then cat "$FIX/pods.after.json"; else cat "$FIX/pods.json"; fi
    exit ;;
  "get secrets")
    if [ -f "$FIX/secrets-read-fails" ]; then echo 'Unable to connect to the server: i/o timeout' >&2; exit 1; fi
    if [ -f "$FIX/waited" ] && [ -f "$FIX/secrets.after.json" ]; then cat "$FIX/secrets.after.json"
    elif [ -f "$FIX/secrets.json" ]; then cat "$FIX/secrets.json"
    else echo '{"items":[]}'; fi
    exit ;;
  "get "*)
    if [ -f "$FIX/obj-read-fails" ]; then echo 'Unable to connect to the server: i/o timeout' >&2; exit 1; fi
    if [ -f "$FIX/obj/$2_$4_$5.json" ]; then cat "$FIX/obj/$2_$4_$5.json"; exit 0; fi
    echo "Error from server (NotFound): \"$5\" not found" >&2
    exit 1 ;;
esac
if [ "$1" = wait ]; then
  : >"$FIX/wait-called"
  # wait-errors holds how many more waits fail as an unreachable apiserver;
  # after those, wait-ok decides between success and kubectl's timeout error.
  n=$(cat "$FIX/wait-errors" 2>/dev/null || echo 0)
  if [ "$n" -gt 0 ]; then
    echo $((n - 1)) >"$FIX/wait-errors"
    if [ -f "$FIX/wait-warns" ]; then echo 'Warning: v1 ComponentStatus is deprecated in v1.19+' >&2; fi
    echo 'error: Get "https://10.96.0.1:443/api/v1/namespaces/tenant-test/pods": dial tcp 10.96.0.1:443: connect: connection refused' >&2
    exit 1
  fi
  # wait-deadline: the deadline passes before the watch starts, so kubectl
  # runs for its whole --timeout and reports the client's error, not its own
  # timeout text.
  if [ -f "$FIX/wait-deadline" ]; then
    for a in "$@"; do
      case $a in --timeout=*) t=${a#--timeout=}; sleep "${t%s}" ;; esac
    done
    echo 'error: Get "https://10.96.0.1:443/api/v1/namespaces/tenant-test/pods/x": context deadline exceeded' >&2
    exit 1
  fi
  if [ -f "$FIX/wait-ok" ]; then
    : >"$FIX/waited"
    exit 0
  fi
  echo 'error: timed out waiting for the condition on pods/x' >&2
  exit 1
fi
exit 99
EOF
  cat >"$FIX/bin/chainsaw" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FIX/chainsaw-calls"
while [ $# -gt 0 ]; do
  case $1 in
    --report-path) path=$2; shift 2 ;;
    --report-name) name=$2; shift 2 ;;
    *) shift ;;
  esac
done
# Named the way chainsaw's getFile names it: .xml is appended only to a name
# with no extension of its own.
case ${name##*/} in
  *.*) file=$path/$name ;;
  *) file=$path/$name.xml ;;
esac
base=${name%.xml}
if [ -f "$FIX/leak-after-$base" ]; then cp "$FIX/leak-after-$base" "$FIX/pods.json"; fi
if [ -f "$FIX/secrets-after-$base" ]; then cp "$FIX/secrets-after-$base" "$FIX/secrets.json"; fi
# The apiserver clock moves on while a suite runs.
if [ -f "$FIX/now-after-$base" ]; then cp "$FIX/now-after-$base" "$FIX/now"; fi
printf '<testsuites name="chainsaw-report" time="1.5" tests="1">\n  <testsuite name="%s" tests="1" failures="0" errors="0" id="0" time="">\n    <testcase name="%s" classname="" time="1.5"></testcase>\n  </testsuite>\n</testsuites>' "$base" "$base" >"$file"
exit 0
EOF
  chmod +x "$FIX/bin/kubectl" "$FIX/bin/chainsaw"
}

# meta NAME NS CREATED [OWNER_API OWNER_KIND OWNER_NAME] — an object's metadata.
meta() {
  owners=""
  if [ -n "${4:-}" ]; then
    owners=$(printf ',"ownerReferences":[{"apiVersion":"%s","kind":"%s","name":"%s","controller":true}]' "$4" "$5" "$6")
  fi
  printf '"metadata":{"name":"%s","namespace":"%s","uid":"uid-%s","creationTimestamp":"%s","labels":{"app.kubernetes.io/instance":"inst-%s"}%s}' \
    "$1" "$2" "$1" "$3" "$1" "$owners"
}

# owner API KIND NAME NS CREATED [OWNER_API OWNER_KIND OWNER_NAME] — put an
# owner object where the stub's `get` finds it.
owner() {
  case $1 in
    */*) res="$2.${1#*/}.${1%%/*}" ;;
    *) res=$2 ;;
  esac
  printf '{"apiVersion":"%s","kind":"%s",%s}' "$1" "$2" "$(meta "$3" "$4" "$5" "${6:-}" "${7:-}" "${8:-}")" >"$FIX/obj/${res}_$4_$3.json"
}

# pod NAME NS PHASE CREATED [OWNER_API OWNER_KIND OWNER_NAME] — one pod object.
pod() {
  printf '{"kind":"Pod",%s,"status":{"phase":"%s"}}' "$(meta "$1" "$2" "$4" "${5:-}" "${6:-}" "${7:-}")" "$3"
}

# pods FILE POD... — a pod list.
pods() {
  f=$1
  shift
  printf '{"items":[' >"$f"
  sep=""
  for p in "$@"; do
    printf '%s%s' "$sep" "$p" >>"$f"
    sep=","
  done
  printf ']}' >>"$f"
}

# The platform: an etcd pod whose Deployment predates every suite.
platform() {
  owner apps/v1 Deployment etcd tenant-root "$T_OLD"
  owner apps/v1 ReplicaSet etcd-rs tenant-root "$T_OLD" apps/v1 Deployment etcd
  pod etcd-0 tenant-root Running "$T_OLD" apps/v1 ReplicaSet etcd-rs
}

# The leak this guard exists for: a broker whose StrimziPodSet and Kafka CR the
# suite's release created, still running after the suite's cleanup.
kafka_leak() {
  owner kafka.strimzi.io/v1beta2 Kafka kafka-test tenant-test 2026-09-24T15:10:30Z
  owner core.strimzi.io/v1beta2 StrimziPodSet kafka-test-kafka tenant-test 2026-09-24T15:11:00Z kafka.strimzi.io/v1beta2 Kafka kafka-test
  pod kafka-test-kafka-0 tenant-test Running 2026-09-24T15:11:05Z core.strimzi.io/v1beta2 StrimziPodSet kafka-test-kafka
}

baseline_of_platform() {
  printf '["uid-etcd-0"]'
}

# secret NAME NS CREATED [CERTIFICATE [OWNER_API OWNER_KIND OWNER_NAME]] prints
# one kubernetes.io/tls Secret, as cert-manager writes it for CERTIFICATE when
# one is named. Its key reads S0VZ, so a test can tell whether key material got
# past the list.
secret() {
  ann=""
  owners=""
  if [ -n "${4:-}" ]; then
    ann=$(printf ',"annotations":{"cert-manager.io/certificate-name":"%s","cert-manager.io/issuer-name":"%s-issuer"}' "$4" "$4")
  fi
  if [ -n "${5:-}" ]; then
    owners=$(printf ',"ownerReferences":[{"apiVersion":"%s","kind":"%s","name":"%s","controller":true}]' "$5" "$6" "$7")
  fi
  printf '{"apiVersion":"v1","kind":"Secret","type":"kubernetes.io/tls","metadata":{"name":"%s","namespace":"%s","uid":"uid-%s","creationTimestamp":"%s"%s%s},"data":{"ca.crt":"Q0E=","tls.crt":"Q0VSVA==","tls.key":"S0VZ"}}' \
    "$1" "$2" "$1" "$3" "$ann" "$owners"
}

# secrets FILE SECRET... writes a Secret list.
secrets() {
  pods "$@"
}

# The leak cozystack/cozystack#4704 reports, the Secret a qdrant release's
# Certificate wrote, still there once the release and the Certificate are gone.
qdrant_leak() {
  secret qdrant-test-tls tenant-test 2026-09-24T15:11:20Z qdrant-test-tls
}

# A Secret cert-manager wrote at install, for a Certificate that still exists.
platform_secret() {
  owner cert-manager.io/v1 Certificate cozystack-api-cert cozy-system "$T_OLD"
  secret cozystack-api-cert cozy-system "$T_OLD" cozystack-api-cert
}

@test "a pod whose root the suite created and that is still running is charged to the suite" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  out=$(lg lg_leaks "$T_START" "$(baseline_of_platform)")
  echo "$out" | grep -Fq 'tenant-test/kafka-test-kafka-0 phase=Running deleting=no root=Kafka/tenant-test/kafka-test rootCreated=2026-09-24T15:10:30Z instance=inst-kafka-test-kafka-0'
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  rm -rf "$FIX"
}

@test "a platform pod replaced mid-suite is not charged because its root is older than the suite" {
  new_fixture
  platform >/dev/null
  pods "$FIX/pods.json" "$(pod etcd-1 tenant-root Running 2026-09-24T15:12:00Z apps/v1 ReplicaSet etcd-rs)"
  out=$(lg lg_leaks "$T_START" '[]')
  [ -z "$out" ]
  rm -rf "$FIX"
}

@test "a root created in the very second the suite started is charged to the suite" {
  new_fixture
  owner apps/v1 StatefulSet same-second tenant-test "$T_START"
  pods "$FIX/pods.json" "$(pod same-second-0 tenant-test Running "$T_START" apps/v1 StatefulSet same-second)"
  out=$(lg lg_leaks "$T_START" '[]')
  echo "$out" | grep -Fq 'root=StatefulSet/tenant-test/same-second'
  rm -rf "$FIX"
}

@test "a pod present before the suite is not charged even when a controller recreated mid-suite adopted it" {
  new_fixture
  # A StatefulSet deleted with --cascade=orphan and recreated adopts the pods it
  # left: the root is new, the pod is not the suite's.
  owner apps/v1 StatefulSet adopter tenant-root 2026-09-24T15:20:00Z
  pods "$FIX/pods.json" "$(pod adopted-0 tenant-root Running "$T_OLD" apps/v1 StatefulSet adopter)"
  out=$(lg lg_leaks "$T_START" '["uid-adopted-0"]')
  [ -z "$out" ]
  rm -rf "$FIX"
}

@test "a finished pod holds no requests and is not charged" {
  new_fixture
  owner batch/v1 Job hook tenant-test 2026-09-24T15:11:00Z
  pods "$FIX/pods.json" \
    "$(pod hook-a tenant-test Succeeded 2026-09-24T15:11:00Z batch/v1 Job hook)" \
    "$(pod hook-b tenant-test Failed 2026-09-24T15:11:00Z batch/v1 Job hook)"
  out=$(lg lg_leaks "$T_START" '[]')
  [ -z "$out" ]
  rm -rf "$FIX"
}

@test "a pod whose owner is gone is charged by the last object the walk reached" {
  new_fixture
  pods "$FIX/pods.json" "$(pod orphan-0 tenant-test Running 2026-09-24T15:11:00Z apps/v1 ReplicaSet vanished)"
  out=$(lg lg_leaks "$T_START" '[]')
  echo "$out" | grep -Fq 'root=Pod/tenant-test/orphan-0 (owner ReplicaSet/vanished missing)'
  rm -rf "$FIX"
}

@test "an owner read that fails for any reason but NotFound is an error and charges nothing" {
  new_fixture
  platform >/dev/null
  pods "$FIX/pods.json" "$(pod etcd-1 tenant-root Running 2026-09-24T15:12:00Z apps/v1 ReplicaSet etcd-rs)"
  : >"$FIX/obj-read-fails"
  if out=$(lg lg_leaks "$T_START" '[]' 2>&1); then
    echo "an unreadable owner was accepted: $out" >&2
    return 1
  fi
  echo "$out" | grep -Fq 'could not read owner ReplicaSet/etcd-rs in tenant-root'
  [ "$(echo "$out" | grep -c 'phase=')" -eq 0 ]
  rm -rf "$FIX"
}

@test "the owner walk follows six links to the real root and stops at the seventh" {
  new_fixture
  # r0 is the root and each r(i) is owned by r(i-1). A pod owned by r5 is six
  # links from r0, one owned by r6 seven. The root predates the suite, every
  # link below it does not, so only a walk that reaches r0 clears the pod.
  owner example.com/v1 Link r0 tenant-test "$T_OLD"
  i=1
  while [ "$i" -le 6 ]; do
    owner example.com/v1 Link "r$i" tenant-test 2026-09-24T15:11:00Z example.com/v1 Link "r$((i - 1))"
    i=$((i + 1))
  done
  pods "$FIX/pods.json" "$(pod six-links tenant-test Running 2026-09-24T15:11:00Z example.com/v1 Link r5)"
  [ -z "$(lg lg_leaks "$T_START" '[]')" ]
  pods "$FIX/pods.json" "$(pod seven-links tenant-test Running 2026-09-24T15:11:00Z example.com/v1 Link r6)"
  lg lg_leaks "$T_START" '[]' | grep -Fq 'root=Link/tenant-test/r1 (depth-cap)'
  rm -rf "$FIX"
}

@test "an unreadable pod list is an error and never an empty answer" {
  new_fixture
  if lg lg_leaks "$T_START" '[]'; then
    echo "lg_leaks succeeded without a pod list" >&2
    return 1
  fi
  if lg lg_check_suite some-suite "$T_START" '[]' 0 /dev/null; then
    echo "lg_check_suite passed a suite it could not check" >&2
    return 1
  fi
  rm -rf "$FIX"
}

@test "a cert-manager Secret whose Certificate is gone is charged to the suite that created it" {
  new_fixture
  secrets "$FIX/secrets.json" "$(platform_secret)" "$(qdrant_leak)"
  out=$(lg lg_secret_leaks "$T_START")
  echo "$out" | grep -Fxq '  secret/tenant-test/qdrant-test-tls certificate=qdrant-test-tls deleting=no root=Secret/tenant-test/qdrant-test-tls (owner Certificate/qdrant-test-tls missing) rootCreated=2026-09-24T15:11:20Z'
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  grep -Fxq 'get secrets --all-namespaces --field-selector type=kubernetes.io/tls --output json' "$FIX/kubectl-calls"
  rm -rf "$FIX"
}

@test "a Secret re-issued mid-suite for a Certificate the install created is not charged" {
  new_fixture
  owner cert-manager.io/v1 Certificate cozystack-api-cert cozy-system "$T_OLD"
  secrets "$FIX/secrets.json" "$(secret cozystack-api-cert cozy-system 2026-09-24T15:12:00Z cozystack-api-cert)"
  [ -z "$(lg lg_secret_leaks "$T_START")" ]
  # The same Secret with its Certificate gone has nothing older behind it.
  rm "$FIX/obj/Certificate.v1.cert-manager.io_cozy-system_cozystack-api-cert.json"
  lg lg_secret_leaks "$T_START" | grep -Fq 'secret/cozy-system/cozystack-api-cert '
  rm -rf "$FIX"
}

@test "a Secret whose Certificate still exists is charged by the root above that Certificate" {
  new_fixture
  # ingress-shim makes the Certificate a child of the Ingress that asked for it.
  owner networking.k8s.io/v1 Ingress web tenant-test 2026-09-24T15:11:00Z
  owner cert-manager.io/v1 Certificate web-tls tenant-test 2026-09-24T15:11:01Z networking.k8s.io/v1 Ingress web
  secrets "$FIX/secrets.json" "$(secret web-tls tenant-test 2026-09-24T15:11:30Z web-tls)"
  lg lg_secret_leaks "$T_START" | grep -Fq 'secret/tenant-test/web-tls certificate=web-tls deleting=no root=Ingress/tenant-test/web rootCreated=2026-09-24T15:11:00Z'
  rm -rf "$FIX"
}

@test "a Secret older than the suite is not charged even when the suite recreated its Certificate" {
  new_fixture
  # An earlier suite left the Secret behind. This one installed a release of
  # the same name, whose Certificate took the Secret over and is not gone yet.
  owner cert-manager.io/v1 Certificate reused-tls tenant-test 2026-09-24T15:12:00Z
  secrets "$FIX/secrets.json" "$(secret reused-tls tenant-test 2026-09-24T15:09:59Z reused-tls)"
  [ -z "$(lg lg_secret_leaks "$T_START")" ]
  # Created in the second the suite started, it is the suite's.
  secrets "$FIX/secrets.json" "$(secret reused-tls tenant-test "$T_START" reused-tls)"
  lg lg_secret_leaks "$T_START" | grep -Fq 'secret/tenant-test/reused-tls '
  rm -rf "$FIX"
}

@test "a TLS Secret that cert-manager did not write is not charged" {
  new_fixture
  # An operator's own serving certificate carries no certificate-name annotation.
  secrets "$FIX/secrets.json" "$(secret kafka-test-cluster-ca-cert tenant-test 2026-09-24T15:11:00Z)"
  [ -z "$(lg lg_secret_leaks "$T_START")" ]
  secrets "$FIX/secrets.json" "$(secret kafka-test-cluster-ca-cert tenant-test 2026-09-24T15:11:00Z kafka-test-cluster-ca-cert)"
  lg lg_secret_leaks "$T_START" | grep -Fq 'secret/tenant-test/kafka-test-cluster-ca-cert '
  rm -rf "$FIX"
}

@test "a Secret with an owner of its own is walked through that owner rather than its Certificate" {
  new_fixture
  # The Percona operator patches its CR on as the owner of the Secrets
  # cert-manager writes for it, so those go with the CR.
  owner psmdb.percona.com/v1 PerconaServerMongoDB mongodb-test tenant-test 2026-09-24T15:10:30Z
  secrets "$FIX/secrets.json" "$(secret mongodb-test-ssl tenant-test 2026-09-24T15:11:00Z mongodb-test-ssl psmdb.percona.com/v1 PerconaServerMongoDB mongodb-test)"
  lg lg_secret_leaks "$T_START" | grep -Fq 'root=PerconaServerMongoDB/tenant-test/mongodb-test rootCreated=2026-09-24T15:10:30Z'
  rm "$FIX/obj/PerconaServerMongoDB.v1.psmdb.percona.com_tenant-test_mongodb-test.json"
  lg lg_secret_leaks "$T_START" | grep -Fq 'root=Secret/tenant-test/mongodb-test-ssl (owner PerconaServerMongoDB/mongodb-test missing)'
  rm -rf "$FIX"
}

@test "a Certificate read that fails for any reason but NotFound is an error and charges nothing" {
  new_fixture
  secrets "$FIX/secrets.json" "$(qdrant_leak)"
  : >"$FIX/obj-read-fails"
  if out=$(lg lg_secret_leaks "$T_START" 2>&1); then
    echo "an unreadable Certificate was accepted and printed $out" >&2
    return 1
  fi
  echo "$out" | grep -Fq 'could not read owner Certificate/qdrant-test-tls in tenant-test'
  [ "$(echo "$out" | grep -c 'secret/')" -eq 0 ]
  rm -rf "$FIX"
}

@test "an unreadable Secret list is an error and leaves the suite unchecked" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)"
  : >"$FIX/secrets-read-fails"
  if lg lg_secret_leaks "$T_START"; then
    echo "lg_secret_leaks succeeded without a Secret list" >&2
    return 1
  fi
  rc=0
  out=$(lg lg_check_suite qdrant "$T_START" "$(baseline_of_platform)" 0 /dev/null) || rc=$?
  [ "$rc" -eq 2 ]
  echo "$out" | grep -Fq '::error title=leak-guard::suite=qdrant the leak check could not read the cluster'
  rm -rf "$FIX"
}

@test "the Secret list the guard keeps carries no key material" {
  new_fixture
  secrets "$FIX/secrets.json" "$(qdrant_leak)"
  # The input does hold a key, so its absence below is the cut at work.
  grep -Fq '"tls.key":"S0VZ"' "$FIX/secrets.json"
  out=$(lg lg_tls_secrets)
  echo "$out" | grep -Fq '"cert-manager.io/certificate-name":"qdrant-test-tls"'
  [ "$(echo "$out" | grep -c -e S0VZ -e '"data"' -e 'issuer-name')" -eq 0 ]
  rm -rf "$FIX"
}

@test "a leak still present when the budget runs out fails the suite and names what leaked" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  if out=$(lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 0 /dev/null); then
    echo "a leaking suite passed: $out" >&2
    return 1
  fi
  echo "$out" | grep -Fq '::error title=leaked-pods::suite=kafka count=1'
  echo "$out" | grep -Fq 'LEAKED-PODS suite=kafka budget=300s count=1'
  echo "$out" | grep -Fq 'tenant-test/kafka-test-kafka-0 '
  grep -Fq 'wait --for=delete pod/kafka-test-kafka-0 --namespace tenant-test --timeout=' "$FIX/kubectl-calls"
  rm -rf "$FIX"
}

@test "leftovers of a suite that already failed are reported once without claiming the budget ran out" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  out=$(lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 1 /dev/null)
  echo "$out" | grep -Fq '::warning title=leaked-pods (suite already failed)::suite=kafka count=1'
  echo "$out" | grep -Fq 'LEAKED-PODS suite=kafka checked-once=chainsaw-failed count=1'
  echo "$out" | grep -Fq 'tenant-test/kafka-test-kafka-0 '
  [ "$(echo "$out" | grep -c -e 'by more than' -e 'budget=')" -eq 0 ]
  [ "$(grep -c '^wait ' "$FIX/kubectl-calls")" -eq 0 ]
  rm -rf "$FIX"
}

@test "a suite the runner could not take a pre-suite reading for is reported as not run" {
  new_fixture
  mkdir -p "$FIX/root/aaa"
  : >"$FIX/root/aaa/chainsaw-test.yaml"
  printf '# none\n' >"$FIX/allow"
  rc=0
  (cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN") || rc=$?
  [ "$rc" -eq 1 ]
  [ ! -f "$FIX/chainsaw-calls" ]
  grep -Fq 'could not take the pre-suite reading; chainsaw did not run' "$FIX/root/chainsaw-report.xml"
  rm -rf "$FIX"
}

@test "a pod that finishes tearing down inside the budget passes the suite" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  pods "$FIX/pods.after.json" "$(platform)"
  : >"$FIX/wait-ok"
  out=$(lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 0 /dev/null)
  echo "$out" | grep -Fq 'leak-guard: suite=kafka clear after'
  [ "$(echo "$out" | grep -c 'LEAKED-PODS')" -eq 0 ]
  rm -rf "$FIX"
}

@test "a wait that fails on the apiserver stops waiting and is reported inconclusive, not as a leak" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  echo 1 >"$FIX/wait-errors"
  out=$(lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 0 /dev/null)
  echo "$out" | grep -Fq '::warning title=leaked-pods (inconclusive)::suite=kafka count=1'
  echo "$out" | grep -Fq 'LEAKED-PODS suite=kafka count=1 inconclusive="error: Get "https://10.96.0.1:443/api/v1/namespaces/tenant-test/pods": dial tcp 10.96.0.1:443: connect: connection refused"'
  [ "$(echo "$out" | grep -c -e 'budget=' -e '::error')" -eq 0 ]
  [ "$(grep -c '^wait ' "$FIX/kubectl-calls")" -eq 1 ]
  rm -rf "$FIX"
}

@test "an inconclusive result names the error, not a warning kubectl printed ahead of it" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  echo 1 >"$FIX/wait-errors"
  : >"$FIX/wait-warns"
  out=$(lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 0 /dev/null)
  echo "$out" | grep -Fq 'inconclusive="error: Get "https://10.96.0.1:443/api/v1/namespaces/tenant-test/pods": dial tcp 10.96.0.1:443: connect: connection refused"'
  [ "$(echo "$out" | grep -c 'inconclusive="Warning:')" -eq 0 ]
  rm -rf "$FIX"
}

@test "a wait that runs its whole timeout and ends on the client deadline is a timeout, so the leak fails" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  : >"$FIX/wait-deadline"
  if out=$(COZY_LEAK_BUDGET=2 lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 0 /dev/null); then
    echo "a leak that outlived the budget passed: $out" >&2
    return 1
  fi
  echo "$out" | grep -Fq '::error title=leaked-pods::suite=kafka count=1'
  [ "$(echo "$out" | grep -c 'inconclusive')" -eq 0 ]
  rm -rf "$FIX"
}

@test "a leak whose wait timed out stays a leak when the next pod list cannot be read" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  echo 1 >"$FIX/pods-failures"
  if out=$(lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 0 /dev/null); then
    echo "a timed-out leak passed: $out" >&2
    return 1
  fi
  echo "$out" | grep -Fq '::error title=leaked-pods::suite=kafka count=1'
  [ "$(echo "$out" | grep -c 'inconclusive')" -eq 0 ]
  rm -rf "$FIX"
}

@test "a pod list that fails after a completed wait is reported inconclusive, not as a leak" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  : >"$FIX/wait-ok"
  echo 1 >"$FIX/pods-failures"
  # A short budget, so that a guard which kept waiting after the failed list
  # ends on the clock within seconds instead of spinning for five minutes.
  out=$(COZY_LEAK_BUDGET=3 lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 0 /dev/null)
  echo "$out" | grep -Fq 'LEAKED-PODS suite=kafka count=1 inconclusive="the re-list after the waits failed because a pod or Secret list or an owner could not be read"'
  [ "$(echo "$out" | grep -c 'could not finish')" -eq 0 ]
  [ "$(echo "$out" | grep -c '::error')" -eq 0 ]
  rm -rf "$FIX"
}

@test "an allowlisted suite reports its leak as a warning and passes without waiting" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  printf '# comment\nkafka cozystack/cozystack#4488\n' >"$FIX/allow"
  out=$(lg lg_check_suite kafka "$T_START" "$(baseline_of_platform)" 0 "$FIX/allow")
  echo "$out" | grep -Fq '::warning title=leaked-pods (allowlisted cozystack/cozystack#4488)::suite=kafka count=1'
  echo "$out" | grep -Fq 'tenant-test/kafka-test-kafka-0 '
  [ "$(grep -c '^wait ' "$FIX/kubectl-calls")" -eq 0 ]
  # The same leak under a suite the list does not name still fails.
  if lg lg_check_suite kafka-metadata "$T_START" "$(baseline_of_platform)" 0 "$FIX/allow" >/dev/null; then
    echo "a suite the allowlist does not name passed" >&2
    return 1
  fi
  rm -rf "$FIX"
}

@test "a cert-manager Secret still present when the budget runs out fails the suite under its own marker" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)"
  secrets "$FIX/secrets.json" "$(platform_secret)" "$(qdrant_leak)"
  if out=$(lg lg_check_suite qdrant "$T_START" "$(baseline_of_platform)" 0 /dev/null); then
    echo "a suite that left a Secret behind passed and printed $out" >&2
    return 1
  fi
  echo "$out" | grep -Fq '::error title=leaked-secrets::suite=qdrant count=1 cert-manager Secrets created by this suite outlived its cleanup by more than 300s'
  echo "$out" | grep -Fq 'LEAKED-SECRETS suite=qdrant budget=300s count=1'
  echo "$out" | grep -Fq '  secret/tenant-test/qdrant-test-tls '
  [ "$(echo "$out" | grep -c -i 'leaked-pods')" -eq 0 ]
  grep -Fq 'wait --for=delete secret/qdrant-test-tls --namespace tenant-test --timeout=' "$FIX/kubectl-calls"
  rm -rf "$FIX"
}

@test "a Secret deleted inside the budget passes the suite" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)"
  secrets "$FIX/secrets.json" "$(qdrant_leak)"
  secrets "$FIX/secrets.after.json"
  : >"$FIX/wait-ok"
  out=$(lg lg_check_suite qdrant "$T_START" "$(baseline_of_platform)" 0 /dev/null)
  echo "$out" | grep -Fq 'leak-guard: suite=qdrant clear after'
  [ "$(echo "$out" | grep -c 'LEAKED-')" -eq 0 ]
  grep -Fq 'wait --for=delete secret/qdrant-test-tls --namespace tenant-test --timeout=' "$FIX/kubectl-calls"
  rm -rf "$FIX"
}

@test "a suite that leaves both pods and Secrets behind reports each under its own marker" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)" "$(kafka_leak)"
  secrets "$FIX/secrets.json" "$(qdrant_leak)" "$(secret qdrant-test-ca tenant-test 2026-09-24T15:11:10Z qdrant-test-ca)"
  rc=0
  out=$(lg lg_check_suite qdrant "$T_START" "$(baseline_of_platform)" 0 /dev/null) || rc=$?
  [ "$rc" -eq 1 ]
  echo "$out" | grep -Fq 'LEAKED-PODS suite=qdrant budget=300s count=1'
  echo "$out" | grep -Fq 'LEAKED-SECRETS suite=qdrant budget=300s count=2'
  # The lines between the pod marker and the next annotation are pods only.
  [ "$(echo "$out" | sed -n '/^LEAKED-PODS/,/^::/p' | grep -c 'kafka-test-kafka-0')" -eq 1 ]
  [ "$(echo "$out" | sed -n '/^LEAKED-PODS/,/^::/p' | grep -c 'secret/')" -eq 0 ]
  rm -rf "$FIX"
}

@test "Secrets of a suite that already failed, or whose wait failed, are reported without failing it" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)"
  secrets "$FIX/secrets.json" "$(qdrant_leak)"
  out=$(lg lg_check_suite qdrant "$T_START" "$(baseline_of_platform)" 1 /dev/null)
  echo "$out" | grep -Fq '::warning title=leaked-secrets (suite already failed)::suite=qdrant count=1 cert-manager Secrets of this failed suite were present right after its cleanup'
  echo "$out" | grep -Fq 'LEAKED-SECRETS suite=qdrant checked-once=chainsaw-failed count=1'
  echo 1 >"$FIX/wait-errors"
  out=$(lg lg_check_suite qdrant "$T_START" "$(baseline_of_platform)" 0 /dev/null)
  echo "$out" | grep -Fq '::warning title=leaked-secrets (inconclusive)::suite=qdrant count=1'
  [ "$(echo "$out" | grep -c '::error')" -eq 0 ]
  rm -rf "$FIX"
}

@test "an allowlisted suite reports its leaked Secrets as a warning and passes without waiting" {
  new_fixture
  pods "$FIX/pods.json" "$(platform)"
  secrets "$FIX/secrets.json" "$(qdrant_leak)"
  printf 'qdrant cozystack/cozystack#4704\n' >"$FIX/allow"
  out=$(lg lg_check_suite qdrant "$T_START" "$(baseline_of_platform)" 0 "$FIX/allow")
  echo "$out" | grep -Fq '::warning title=leaked-secrets (allowlisted cozystack/cozystack#4704)::suite=qdrant count=1'
  echo "$out" | grep -Fq 'LEAKED-SECRETS suite=qdrant count=1 allowlisted=cozystack/cozystack#4704'
  [ "$(grep -c '^wait ' "$FIX/kubectl-calls")" -eq 0 ]
  rm -rf "$FIX"
}

@test "an allowlist entry for a suite directory that does not exist stops the run" {
  new_fixture
  mkdir -p "$FIX/root/kafka"
  : >"$FIX/root/kafka/chainsaw-test.yaml"
  printf 'kafka cozystack/cozystack#4488\n' >"$FIX/allow"
  lg lg_allowlist_validate "$FIX/allow" "$FIX/root"
  printf 'kafka cozystack/cozystack#4488\nkafka-renamed cozystack/cozystack#4488\n' >"$FIX/allow"
  if lg lg_allowlist_validate "$FIX/allow" "$FIX/root"; then
    echo "a stale allowlist entry was accepted" >&2
    return 1
  fi
  printf 'kafka\n' >"$FIX/allow"
  if lg lg_allowlist_validate "$FIX/allow" "$FIX/root"; then
    echo "an allowlist entry without an issue ref was accepted" >&2
    return 1
  fi
  rm -rf "$FIX"
}

@test "an allowlist whose last line has no newline still has that line validated" {
  new_fixture
  mkdir -p "$FIX/root/kafka"
  : >"$FIX/root/kafka/chainsaw-test.yaml"
  printf 'kafka cozystack/cozystack#4488\nkafka-renamed cozystack/cozystack#4488' >"$FIX/allow"
  if lg lg_allowlist_validate "$FIX/allow" "$FIX/root"; then
    echo "a stale last entry without a newline was accepted" >&2
    return 1
  fi
  rm -rf "$FIX"
}

@test "the shipped allowlist names only suite directories that exist" {
  new_fixture
  lg lg_allowlist_validate "$HACK_DIR/e2e-chainsaw/_lib/leak-allowlist.txt" "$HACK_DIR/e2e-chainsaw"
  rm -rf "$FIX"
}

@test "the leak budget equals the default cleanup timeout in the chainsaw configuration" {
  new_fixture
  cleanup=$(yq '.spec.timeouts.cleanup' "$HACK_DIR/e2e-chainsaw/.chainsaw.yaml")
  [ "$cleanup" = 5m ]
  [ "$(bash -c '. "$LG_LIB"; echo "$LG_BUDGET"')" = 300 ]
  [ "$(bash -c '. "$LG_LIB"; echo "$LG_DEPTH_CAP"')" = 6 ]
  rm -rf "$FIX"
}

@test "merged report sums the per-suite reports and keeps each failure" {
  new_fixture
  mkdir "$FIX/r"
  cat >"$FIX/r/redis.xml" <<'EOF'
<testsuites name="chainsaw-report" time="474.868461" tests="2">
  <testsuite name="redis" tests="2" failures="0" errors="0" id="0" time="">
    <testcase name="redis-tls" classname="" time="75.267668"></testcase>
    <testcase name="redis" classname="" time="57.092179"></testcase>
  </testsuite>
</testsuites>
EOF
  cat >"$FIX/r/harbor.xml" <<'EOF'
<testsuites name="chainsaw-report" time="900.5" tests="1" failures="1">
  <testsuite name="harbor" tests="1" failures="1" errors="0" id="0" time="">
    <testcase name="harbor" classname="" time="900.5">
      <failure message="helm.toolkit.fluxcd.io/v2/HelmRelease/tenant-test/harbor-test&#xA;* status: Invalid value: &#34;Unknown&#34;&#xA;; context deadline exceeded"></failure>
    </testcase>
  </testsuite>
</testsuites>
EOF
  : >"$FIX/r/harbor.leaked"
  lg lg_merge_reports "$FIX/r" redis harbor never-reported >"$FIX/merged.xml"
  head -n 1 "$FIX/merged.xml" | grep -Fxq '<testsuites name="chainsaw-report" time="1375.368461" tests="5" failures="3">'
  grep -Fq '<testcase name="redis-tls" classname="" time="75.267668"></testcase>' "$FIX/merged.xml"
  grep -Fq 'context deadline exceeded"></failure>' "$FIX/merged.xml"
  grep -Fq '<testcase name="leak-guard" classname="" time="">' "$FIX/merged.xml"
  grep -Fq 'chainsaw wrote no report for this suite' "$FIX/merged.xml"
  [ "$(grep -c '<testsuite ' "$FIX/merged.xml")" -eq 4 ]
  python3 -c 'import sys, xml.dom.minidom; xml.dom.minidom.parse(sys.argv[1])' "$FIX/merged.xml"
  rm -rf "$FIX"
}

@test "the runner fails a leaking suite and still runs and reports every other suite" {
  new_fixture
  mkdir -p "$FIX/root/aaa" "$FIX/root/bbb" "$FIX/root/_lib"
  : >"$FIX/root/aaa/chainsaw-test.yaml"
  : >"$FIX/root/bbb/chainsaw-test.yaml"
  printf '# none\n' >"$FIX/allow"
  pods "$FIX/pods.json" "$(platform)"
  # The stub chainsaw leaves the broker behind when it runs suite aaa.
  pods "$FIX/leak-after-aaa" "$(platform)" "$(kafka_leak)"
  rc=0
  out=$(cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN" --set-string storageClass=local 2>&1) || rc=$?
  [ "$rc" -eq 1 ]
  echo "$out" | grep -Fq 'LEAKED-PODS suite=aaa budget=300s count=1'
  # bbb ran after aaa's leak and is not charged with it: its root is older.
  echo "$out" | grep -Fq 'leak-guard: suite=bbb clear after'
  grep -Fq 'leak-guard' "$FIX/root/chainsaw-report.xml"
  grep -Fq '<testsuite name="bbb"' "$FIX/root/chainsaw-report.xml"
  rm -rf "$FIX"
}

@test "the runner fails a suite that leaves a cert-manager Secret behind and not the suite after it" {
  new_fixture
  mkdir -p "$FIX/root/aaa" "$FIX/root/bbb"
  : >"$FIX/root/aaa/chainsaw-test.yaml"
  : >"$FIX/root/bbb/chainsaw-test.yaml"
  printf '# none\n' >"$FIX/allow"
  pods "$FIX/pods.json" "$(platform)"
  # The stub chainsaw leaves the Secret behind when it runs suite aaa, and the
  # apiserver clock has moved past it by the time bbb starts.
  secrets "$FIX/secrets-after-aaa" "$(qdrant_leak)"
  printf '2026-09-24T15:30:00Z' >"$FIX/now-after-aaa"
  rc=0
  out=$(cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN" 2>&1) || rc=$?
  [ "$rc" -eq 1 ]
  echo "$out" | grep -Fq 'LEAKED-SECRETS suite=aaa budget=300s count=1'
  echo "$out" | grep -Fq 'leak-guard: suite=bbb clear after'
  grep -Fq 'the leak guard failed this suite. See the leaked-pods or leaked-secrets lines for suite=aaa' "$FIX/root/chainsaw-report.xml"
  rm -rf "$FIX"
}

@test "the runner reports a cluster it could not read after a suite as unchecked, not as a leak" {
  new_fixture
  mkdir -p "$FIX/root/aaa"
  : >"$FIX/root/aaa/chainsaw-test.yaml"
  printf '# none\n' >"$FIX/allow"
  pods "$FIX/pods.json" "$(platform)"
  # The stub chainsaw leaves a pod list that is not JSON, so the reading taken
  # after the suite fails while the one before it succeeded.
  printf 'not json' >"$FIX/leak-after-aaa"
  rc=0
  out=$(cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN" 2>&1) || rc=$?
  [ "$rc" -eq 1 ]
  echo "$out" | grep -Fq '::error title=leak-guard::suite=aaa the leak check could not read the cluster'
  grep -Fq 'the leak guard could not read the cluster after this suite; see the leak-guard annotation for suite=aaa' "$FIX/root/chainsaw-report.xml"
  [ "$(grep -c 'the leak guard failed this suite' "$FIX/root/chainsaw-report.xml")" -eq 0 ]
  rm -rf "$FIX"
}

@test "the runner keeps the report of a suite whose directory name has a dot" {
  new_fixture
  mkdir -p "$FIX/root/app.v2"
  : >"$FIX/root/app.v2/chainsaw-test.yaml"
  printf '# none\n' >"$FIX/allow"
  pods "$FIX/pods.json" "$(platform)"
  (cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN")
  grep -Fq '<testcase name="app.v2" classname="" time="1.5"></testcase>' "$FIX/root/chainsaw-report.xml"
  [ "$(grep -c 'chainsaw wrote no report' "$FIX/root/chainsaw-report.xml")" -eq 0 ]
  rm -rf "$FIX"
}

@test "the runner passes a clean run" {
  new_fixture
  mkdir -p "$FIX/root/aaa"
  : >"$FIX/root/aaa/chainsaw-test.yaml"
  printf '# none\n' >"$FIX/allow"
  pods "$FIX/pods.json" "$(platform)"
  (cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN")
  head -n 1 "$FIX/root/chainsaw-report.xml" | grep -Fxq '<testsuites name="chainsaw-report" time="1.500000" tests="1">'
  rm -rf "$FIX"
}

@test "the runner hands chainsaw exactly the suites and flags a single invocation would get" {
  new_fixture
  mkdir -p "$FIX/root/bbb" "$FIX/root/aaa" "$FIX/root/_lib"
  : >"$FIX/root/aaa/chainsaw-test.yaml"
  : >"$FIX/root/bbb/chainsaw-test.yaml"
  printf '# none\n' >"$FIX/allow"
  pods "$FIX/pods.json" "$(platform)"
  # Empty selection: every directory under the root, in a fixed order, each to
  # chainsaw, whose discovery finds nothing in _lib just as it would from `.`.
  (cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN" --set-string storageClass=local)
  sed 's/ --report-path [^ ]* --report-name [^ ]* / /' "$FIX/chainsaw-calls" >"$FIX/calls"
  printf 'test --set-string storageClass=local _lib\ntest --set-string storageClass=local aaa\ntest --set-string storageClass=local bbb\n' >"$FIX/want"
  cmp "$FIX/want" "$FIX/calls"
  # A selection is run as given, in the given order, and nothing else.
  : >"$FIX/chainsaw-calls"
  (cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN" --set-string storageClass=local bbb aaa/)
  sed 's/ --report-path [^ ]* --report-name [^ ]* / /' "$FIX/chainsaw-calls" >"$FIX/calls"
  printf 'test --set-string storageClass=local bbb\ntest --set-string storageClass=local aaa\n' >"$FIX/want"
  cmp "$FIX/want" "$FIX/calls"
  rm -rf "$FIX"
}

@test "the runner refuses a tree whose top level holds a test no suite would run" {
  new_fixture
  mkdir -p "$FIX/root/aaa"
  : >"$FIX/root/chainsaw-test.yaml"
  printf '# none\n' >"$FIX/allow"
  rc=0
  (cd "$FIX/root" && PATH="$FIX/bin:$PATH" COZY_LEAK_ALLOWLIST="$FIX/allow" bash "$LG_RUN") || rc=$?
  [ "$rc" -eq 2 ]
  [ ! -f "$FIX/chainsaw-calls" ]
  rm -rf "$FIX"
}

@test "the runner refuses a --set-string with no value" {
  new_fixture
  rc=0
  out=$(PATH="$FIX/bin:$PATH" bash "$LG_RUN" --set-string 2>&1) || rc=$?
  [ "$rc" -eq 2 ]
  echo "$out" | grep -Fq -- '--set-string needs a value'
  rm -rf "$FIX"
}

@test "the e2e target runs the suite through the leak-guarded runner" {
  cmd=$(make -n -C "$HACK_DIR/../packages/core/testing" SANDBOX_NAME=test CHAINSAW_SUITES=kafka test-chainsaw)
  printf '%s\n' "$cmd" | grep -Fq '&& ../e2e-chainsaw-run.sh --set-string storageClass='
  [ "$(printf '%s\n' "$cmd" | grep -c 'chainsaw test')" -eq 0 ]
  printf '%s\n' "$cmd" | grep -Fq ':-replicated}" kafka'
}
