#!/usr/bin/env bats
# Behavioural unit test for packages/system/cilium/files/ipam-migration.sh, the
# script the cilium chart embeds into its post-upgrade hook Job when ipam.mode
# is cluster-pool.
#
# The script is EXECUTED against a fake kubectl that keeps CiliumNode, Node,
# agent pod and kube-ovn Subnet state in files and models the two controllers
# the script waits on. cilium-operator allocates a /29 from the pool to a
# CiliumNode only once its spec.ipam.podCIDRs is empty, and the agent DaemonSet
# replaces a deleted agent pod with a Ready one whose router and Ingress IPs
# come from the node's current allocation. Either can be told not to act, which
# is how the timeout paths are reached.
#
# hack/cozytest.sh knows @test and bash only, with no setup() or teardown() and
# no run/$status/$output, so each test calls init_state itself and every
# negative assertion is an explicit `if ...; then ...; false; fi`.

SCRIPT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)/packages/system/cilium/files/ipam-migration.sh"

# init_state builds an isolated workspace with the fake kubectl first on PATH,
# the environment the hook Job sets and a kube-ovn default subnet that already
# excludes the pool and has nothing allocated inside it.
init_state() {
  STATE="$(mktemp -d)"
  mkdir -p "$STATE/bin" "$STATE/ciliumnodes" "$STATE/nodes" "$STATE/pods"
  export STATE PATH="$STATE/bin:$PATH" KLOG="$STATE/kubectl.log"
  export NAMESPACE=cozy-cilium POOL=10.244.248.0/21 MASK=29
  export ALLOCATION_TIMEOUT=3 AGENT_TIMEOUT=3 POLL_INTERVAL=1
  export OPERATOR=allocate AGENT=restart
  : > "$KLOG"
  echo 0 > "$STATE/next-block"
  echo '{"items": []}' > "$STATE/ips.json"
  set_subnet '["10.244.0.1","10.244.248.0..10.244.255.255"]' "10.244.0.2-10.244.0.30,10.244.3.7"
  cat > "$STATE/bin/kubectl" <<'KEOF'
#!/bin/bash
set -euo pipefail
echo "$*" >> "$KLOG"
args=()
field= patch=
while [ $# -gt 0 ]; do
  case $1 in
    -n|-l|-o) shift ;;
    --field-selector) field=$2; shift ;;
    -p) patch=$2; shift ;;
    -*) ;;
    *) args+=("$1") ;;
  esac
  shift
done
set -- "${args[@]}"
verb=$1 kind=$2

allocate() {
  local file=$1 n
  if [ "$OPERATOR" != allocate ] || jq -e '.spec.ipam.podCIDRs' "$file" >/dev/null; then
    return 0
  fi
  n=$(cat "$STATE/next-block")
  echo $((n + 1)) > "$STATE/next-block"
  jq --arg c "10.244.248.$((n * 8))/29" '.spec.ipam.podCIDRs = [$c]' "$file" > "$file.tmp"
  mv "$file.tmp" "$file"
  }

case "$verb $kind" in
  "get ciliumnodes")
    for f in "$STATE"/ciliumnodes/*.json; do allocate "$f"; done
    jq -s '{items: .}' "$STATE"/ciliumnodes/*.json
    ;;
  "get ciliumnode")
    name=$3
    if [ "${FAIL_GET_CILIUMNODE:-}" = "$name" ]; then
      echo "error: the server is currently unable to handle the request" >&2
      exit 1
    fi
    allocate "$STATE/ciliumnodes/$name.json"
    cat "$STATE/ciliumnodes/$name.json"
    # RACE_CIDR models a writer that gets in between this read and the patch.
    if [ -n "${RACE_CIDR:-}" ] && [ ! -f "$STATE/raced" ]; then
      touch "$STATE/raced"
      jq --arg c "$RACE_CIDR" '.spec.ipam.podCIDRs = [$c]' "$STATE/ciliumnodes/$name.json" > "$STATE/race.tmp"
      mv "$STATE/race.tmp" "$STATE/ciliumnodes/$name.json"
    fi
    ;;
  "patch ciliumnode")
    name=$3
    file="$STATE/ciliumnodes/$name.json"
    expected=$(jq -c '.[] | select(.op == "test") | .value' <<<"$patch")
    actual=$(jq -c '.spec.ipam.podCIDRs' "$file")
    if [ "$expected" != "$actual" ]; then
      echo "The request is invalid: the server rejected our request due to an error in our request" >&2
      exit 1
    fi
    jq 'del(.spec.ipam.podCIDRs)' "$file" > "$file.tmp"
    mv "$file.tmp" "$file"
    ;;
  "get customresourcedefinition")
    if [ -f "$STATE/subnet.json" ]; then
      echo "customresourcedefinition.apiextensions.k8s.io/subnets.kubeovn.io"
    fi
    ;;
  "get subnets.kubeovn.io")
    if [ -z "${3:-}" ]; then
      jq '{items: [.]}' "$STATE/subnet.json"
      exit 0
    fi
    # A read by name models kube-ovn publishing the status after the patch.
    # OVN_LAG reads go by before status.v4availableIPrange stops offering the
    # end of the /16, and OVN_STUCK means it never does.
    n=$(( $(cat "$STATE/status-reads" 2>/dev/null || echo 0) + 1 ))
    echo "$n" > "$STATE/status-reads"
    if [ -z "${OVN_STUCK:-}" ] && [ "$n" -gt "${OVN_LAG:-0}" ]; then
      jq '.status.v4availableIPrange = "10.244.0.31-10.244.247.255"' "$STATE/subnet.json" > "$STATE/subnet.tmp"
      mv "$STATE/subnet.tmp" "$STATE/subnet.json"
    fi
    cat "$STATE/subnet.json"
    ;;
  "get ips.kubeovn.io")
    cat "$STATE/ips.json"
    ;;
  "patch subnets.kubeovn.io")
    # RACE_SUBNET models kube-ovn rewriting excludeIps between the script's read
    # and its patch, once or on every attempt.
    if [ -n "${RACE_SUBNET:-}" ] && { [ "$RACE_SUBNET" = always ] || [ ! -f "$STATE/subnet-raced" ]; }; then
      touch "$STATE/subnet-raced"
      n=$(( $(cat "$STATE/subnet-races" 2>/dev/null || echo 0) + 1 ))
      echo "$n" > "$STATE/subnet-races"
      jq --arg e "10.244.1.$n" '.spec.excludeIps += [$e]' "$STATE/subnet.json" > "$STATE/subnet.tmp"
      mv "$STATE/subnet.tmp" "$STATE/subnet.json"
    fi
    expected=$(jq -c '.[] | select(.op == "test") | .value' <<<"$patch")
    actual=$(jq -c '.spec.excludeIps' "$STATE/subnet.json")
    if [ "$expected" != "$actual" ]; then
      echo "The request is invalid: the server rejected our request due to an error in our request" >&2
      exit 1
    fi
    jq --argjson v "$(jq -c '.[] | select(.op == "replace") | .value' <<<"$patch")" '.spec.excludeIps = $v' \
      "$STATE/subnet.json" > "$STATE/subnet.tmp"
    mv "$STATE/subnet.tmp" "$STATE/subnet.json"
    ;;
  "get node")
    name=$3
    if [ -f "$STATE/nodes/$name.json" ]; then
      cat "$STATE/nodes/$name.json"
    fi
    ;;
  "get pods")
    node=${field#spec.nodeName=}
    jq -s --arg n "$node" '{items: [.[] | select(.spec.nodeName == $n)]}' "$STATE"/pods/*.json 2>/dev/null || echo '{"items": []}'
    ;;
  "delete pod")
    name=$3
    node=$(jq -r '.spec.nodeName' "$STATE/pods/$name.json")
    rm "$STATE/pods/$name.json"
    if [ "$AGENT" = restart ]; then
      jq -n --arg p "$name-r" --arg n "$node" \
        '{metadata: {name: $p}, spec: {nodeName: $n}, status: {conditions: [{type: "Ready", status: "True"}]}}' \
        > "$STATE/pods/$name-r.json"
      file="$STATE/ciliumnodes/$node.json"
      block=$(jq -r '.spec.ipam.podCIDRs[0] | split("/")[0]' "$file")
      jq --arg r "${block%.*}.$(( ${block##*.} + 2 ))" --arg i "${block%.*}.$(( ${block##*.} + 3 ))" \
        '.spec.addresses = [{type: "InternalIP", ip: "192.0.2.1"}, {type: "CiliumInternalIP", ip: $r}] | .spec.ingress.ipv4 = $i' \
        "$file" > "$file.tmp"
      mv "$file.tmp" "$file"
    fi
    ;;
  *)
    echo "fake kubectl: unexpected call: $verb $kind" >&2
    exit 1
    ;;
esac
KEOF
  chmod +x "$STATE/bin/kubectl"
}

# set_subnet <excludeIps JSON> <status.v4usingIPrange> [status.v4availableIPrange]
# By default status.v4availableIPrange stops short of the pool once excludeIps
# lists it, the way kube-ovn reports it.
set_subnet() {
  available=10.244.0.31-10.244.255.255
  if printf '%s' "$1" | jq -e 'index("10.244.248.0..10.244.255.255")' >/dev/null; then
    available=10.244.0.31-10.244.247.255
  fi
  available=${3:-$available}
  jq -n --argjson e "$1" --arg u "$2" --arg a "$available" \
    '{metadata: {name: "ovn-default"}, spec: {default: true, cidrBlock: "10.244.0.0/16", excludeIps: $e}, status: {v4usingIPrange: $u, v4availableIPrange: $a}}' \
    > "$STATE/subnet.json"
}

# add_ip <IP object name> <subnet> <v4 address>
add_ip() {
  jq --arg n "$1" --arg s "$2" --arg i "$3" '.items += [{metadata: {name: $n}, spec: {subnet: $s, v4IpAddress: $i}}]' \
    "$STATE/ips.json" > "$STATE/ips.tmp"
  mv "$STATE/ips.tmp" "$STATE/ips.json"
}

# add_node <name> <podCIDR or ""> <router IP> <ingress IP> [Ready status]
# Writes a CiliumNode, its Node and one Ready agent pod.
add_node() {
  local name=$1 cidr=$2 router=$3 ingress=$4 ready=${5:-True}
  jq -n --arg n "$name" --arg c "$cidr" --arg r "$router" --arg i "$ingress" '
    {metadata: {name: $n},
     spec: ({addresses: [{type: "InternalIP", ip: "192.0.2.1"}, {type: "CiliumInternalIP", ip: $r}],
             ingress: {ipv4: $i}, ipam: {}}
            | if $c == "" then . else .ipam.podCIDRs = [$c] end)}' > "$STATE/ciliumnodes/$name.json"
  jq -n --arg n "$name" --arg s "$ready" \
    '{metadata: {name: $n}, status: {conditions: [{type: "Ready", status: $s}]}}' > "$STATE/nodes/$name.json"
  jq -n --arg p "cilium-$name" --arg n "$name" \
    '{metadata: {name: $p}, spec: {nodeName: $n}, status: {conditions: [{type: "Ready", status: "True"}]}}' \
    > "$STATE/pods/cilium-$name.json"
}

pod_cidrs_of() {
  jq -c '.spec.ipam.podCIDRs' "$STATE/ciliumnodes/$1.json"
}

@test "stale nodes are moved onto the pool and their agents restarted one at a time" {
  init_state
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  add_node n2 10.244.4.0/24 10.244.4.251 10.244.4.240
  "$SCRIPT"
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  [ "$(pod_cidrs_of n2)" = '["10.244.248.8/29"]' ]
  [ "$(jq -r '.spec.ingress.ipv4' "$STATE/ciliumnodes/n1.json")" = 10.244.248.3 ]
  [ -f "$STATE/pods/cilium-n1-r.json" ]
  [ -f "$STATE/pods/cilium-n2-r.json" ]
  # n1 is cleared, allocated and restarted before n2 is touched at all.
  first_n2=$(grep -n "ciliumnode n2" "$KLOG" | head -1 | cut -d: -f1)
  n1_restart=$(grep -n "delete pod cilium-n1" "$KLOG" | cut -d: -f1)
  [ "$n1_restart" -lt "$first_n2" ]
  rm -rf "$STATE"
}

@test "an agent that came up before the operator allocated its CIDR is restarted" {
  init_state
  # An empty field the operator fills, and an agent still holding addresses
  # from the old /24.
  add_node n1 "" 10.244.2.223 10.244.2.135
  "$SCRIPT"
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  grep -q "^-n cozy-cilium delete pod cilium-n1" "$KLOG"
  rm -rf "$STATE"
}

@test "a node whose agent already took its addresses from the pool is left alone" {
  init_state
  add_node n1 10.244.248.24/29 10.244.248.26 10.244.248.27
  "$SCRIPT"
  if grep -q -e "^patch" -e "delete pod" "$KLOG"; then
    echo "BUG: a node already on the pool, or the subnet that already excludes it, was touched"
    false
  fi
  rm -rf "$STATE"
}

@test "an agent whose Ingress IP alone is still outside its allocation is restarted" {
  init_state
  add_node n1 10.244.248.24/29 10.244.248.26 10.244.2.135
  "$SCRIPT"
  grep -q "delete pod cilium-n1" "$KLOG"
  rm -rf "$STATE"
}

@test "the pool is excluded from the kube-ovn default subnet before any node moves" {
  init_state
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  "$SCRIPT"
  [ "$(jq -c '.spec.excludeIps' "$STATE/subnet.json")" = '["10.244.0.1","10.244.248.0..10.244.255.255","10.244.2.135"]' ]
  exclude=$(grep -n "^patch subnets.kubeovn.io" "$KLOG" | cut -d: -f1)
  clear=$(grep -n "^patch ciliumnode n1" "$KLOG" | cut -d: -f1)
  [ "$exclude" -lt "$clear" ]
  rm -rf "$STATE"
}

@test "the Ingress IPs the agents leave behind go into the same patch as the pool, the router IPs do not" {
  init_state
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  add_node n2 10.244.4.0/24 10.244.4.251 10.244.4.240
  "$SCRIPT"
  [ "$(jq -c '.spec.excludeIps' "$STATE/subnet.json")" = '["10.244.0.1","10.244.248.0..10.244.255.255","10.244.2.135","10.244.4.240"]' ]
  [ "$(grep -c "^patch subnets.kubeovn.io" "$KLOG")" -eq 1 ]
  rm -rf "$STATE"
}

@test "an Ingress IP that already lies in the pool is covered by the pool range alone" {
  init_state
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.248.24/29 10.244.248.26 10.244.248.27
  "$SCRIPT"
  [ "$(jq -c '.spec.excludeIps' "$STATE/subnet.json")" = '["10.244.0.1","10.244.248.0..10.244.255.255"]' ]
  rm -rf "$STATE"
}

@test "an Ingress IP the subnet already excludes is not added again" {
  init_state
  set_subnet '["10.244.0.1","10.244.2.130..10.244.2.140"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  "$SCRIPT"
  [ "$(jq -c '.spec.excludeIps' "$STATE/subnet.json")" = '["10.244.0.1","10.244.2.130..10.244.2.140","10.244.248.0..10.244.255.255"]' ]
  rm -rf "$STATE"
}

@test "a subnet rewritten between the read and the patch is read again and patched on top of the new list" {
  init_state
  export RACE_SUBNET=once
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  "$SCRIPT"
  [ "$(jq -c '.spec.excludeIps' "$STATE/subnet.json")" = '["10.244.0.1","10.244.1.1","10.244.248.0..10.244.255.255","10.244.2.135"]' ]
  [ "$(grep -c "^patch subnets.kubeovn.io" "$KLOG")" -eq 2 ]
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  rm -rf "$STATE"
}

@test "a subnet that keeps changing under the patch stops the run before any node moves" {
  init_state
  export RACE_SUBNET=always
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "kept changing under the patch" "$STATE/out"
  if grep -q "^patch ciliumnode" "$KLOG"; then
    echo "BUG: a node was moved although the pool was never excluded"
    false
  fi
  rm -rf "$STATE"
}

@test "a pool excluded by an earlier attempt that still has pods on it stops the run" {
  init_state
  # kube-ovn leaves excluded addresses out of status.v4usingIPrange, so only
  # the IP object shows the pod still sitting in the pool.
  add_ip db-0.tenant-a ovn-default 10.244.250.7
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "still has db-0.tenant-a=10.244.250.7 in use inside 10.244.248.0/21" "$STATE/out"
  if grep -q "^patch ciliumnode" "$KLOG"; then
    echo "BUG: a node was moved onto a pool a pod still holds an address in"
    false
  fi
  rm -rf "$STATE"
}

@test "an IP object of another subnet with an address in the pool range does not stop the run" {
  init_state
  add_ip vm-0.tenant-b vpc-b-subnet 10.244.250.7
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  "$SCRIPT"
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  rm -rf "$STATE"
}

@test "the IP objects are read only after kube-ovn stops offering the pool" {
  init_state
  export OVN_LAG=2
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  "$SCRIPT"
  [ "$(cat "$STATE/status-reads")" -eq 3 ]
  reads=$(grep -n "^get subnets.kubeovn.io ovn-default" "$KLOG" | tail -1 | cut -d: -f1)
  ips=$(grep -n "^get ips.kubeovn.io" "$KLOG" | cut -d: -f1)
  [ "$reads" -lt "$ips" ]
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  rm -rf "$STATE"
}

@test "a subnet whose status never drops the pool stops the run before any node moves" {
  init_state
  export OVN_STUCK=1
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "kube-ovn has not applied the exclusion" "$STATE/out"
  if grep -q "^patch ciliumnode" "$KLOG"; then
    echo "BUG: a node was moved before kube-ovn applied the exclusion"
    false
  fi
  rm -rf "$STATE"
}

@test "a wider exclusion that already covers the pool is left as it is" {
  init_state
  set_subnet '["10.244.0.1","10.244.240.0..10.244.255.255"]' "10.244.0.2-10.244.0.30" "10.244.0.31-10.244.239.255"
  add_node n1 10.244.248.24/29 10.244.248.26 10.244.248.27
  "$SCRIPT"
  if grep -q "^patch subnets.kubeovn.io" "$KLOG"; then
    echo "BUG: an exclusion that already covers the pool was patched again"
    false
  fi
  rm -rf "$STATE"
}

@test "addresses kube-ovn already handed out from the pool stop the run before any node moves" {
  init_state
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30,10.244.249.4-10.244.249.6"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "has already handed out 10.244.249.4-10.244.249.6 from 10.244.248.0/21" "$STATE/out"
  # The exclusion still lands, so kube-ovn stops handing out more from the pool.
  [ "$(jq -c '.spec.excludeIps' "$STATE/subnet.json")" = '["10.244.0.1","10.244.248.0..10.244.255.255","10.244.2.135"]' ]
  if grep -q "^patch ciliumnode" "$KLOG"; then
    echo "BUG: a node was moved onto a pool kube-ovn still hands addresses from"
    false
  fi
  rm -rf "$STATE"
}

@test "a pool outside the default subnet stops the run before anything changes" {
  init_state
  export POOL=100.65.0.0/16
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "100.65.0.0/16 lies outside subnet ovn-default (10.244.0.0/16)" "$STATE/out"
  if grep -q -e "^patch subnets.kubeovn.io" -e "^patch ciliumnode" "$KLOG"; then
    echo "BUG: something was changed for a pool outside kube-ovn's subnet"
    false
  fi
  rm -rf "$STATE"
}

@test "more CiliumNodes than the pool has blocks stops the run before anything changes" {
  init_state
  export POOL=10.244.248.0/29
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  add_node n2 10.244.4.0/24 10.244.4.251 10.244.4.240
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "there are 2 CiliumNodes but 10.244.248.0/29 holds only 1 /29 blocks" "$STATE/out"
  if grep -q -e "^patch" "$KLOG"; then
    echo "BUG: something was changed although the pool cannot hold every node"
    false
  fi
  rm -rf "$STATE"
}

@test "a pool with exactly one block per node is enough" {
  init_state
  export POOL=10.244.248.0/28
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  add_node n2 10.244.4.0/24 10.244.4.251 10.244.4.240
  "$SCRIPT"
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  [ "$(pod_cidrs_of n2)" = '["10.244.248.8/29"]' ]
  rm -rf "$STATE"
}

@test "a usage range that crosses the start of the pool stops the run" {
  init_state
  # kube-ovn's ascending pointer reaches the block from below, so the first
  # range to touch it starts outside.
  set_subnet '["10.244.0.1"]' "10.244.0.2-10.244.0.30,10.244.247.250-10.244.248.3"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "has already handed out 10.244.247.250-10.244.248.3 from 10.244.248.0/21" "$STATE/out"
  if grep -q "^patch ciliumnode" "$KLOG"; then
    echo "BUG: a node was moved onto a pool kube-ovn still hands addresses from"
    false
  fi
  rm -rf "$STATE"
}

@test "a pod on a node's current Ingress IP is named in the log and does not stop the run" {
  init_state
  add_ip web-0.tenant-c ovn-default 10.244.2.135
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  "$SCRIPT" > "$STATE/out" 2>&1
  grep -q "web-0.tenant-c holds 10.244.2.135, the Ingress IP of n1 before the move" "$STATE/out"
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  rm -rf "$STATE"
}

@test "a cluster without kube-ovn skips the exclusion and still moves its nodes" {
  init_state
  rm "$STATE/subnet.json"
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  "$SCRIPT"
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  if grep -q -e "^get subnets.kubeovn.io" -e "^patch subnets.kubeovn.io" "$KLOG"; then
    echo "BUG: subnets were queried on a cluster that has no kube-ovn"
    false
  fi
  rm -rf "$STATE"
}

@test "the run fails when the operator never allocates, and still handles the other nodes" {
  init_state
  export OPERATOR=none
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  add_node n2 10.244.248.24/29 10.244.248.26 10.244.4.240
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "n1: no CIDR from 10.244.248.0/21" "$STATE/out"
  grep -q "delete pod cilium-n2" "$KLOG"
  rm -rf "$STATE"
}

@test "the run fails when a restarted agent does not come back" {
  init_state
  export AGENT=stuck
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "n1: agent not ready on 10.244.248.0/29" "$STATE/out"
  rm -rf "$STATE"
}

@test "the agent of a node that is not Ready is not restarted" {
  init_state
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135 False
  "$SCRIPT"
  [ "$(pod_cidrs_of n1)" = '["10.244.248.0/29"]' ]
  if grep -q "delete pod" "$KLOG"; then
    echo "BUG: the agent of a NotReady node was deleted"
    false
  fi
  rm -rf "$STATE"
}

@test "a failed CiliumNode read stops the run before anything is cleared" {
  init_state
  export FAIL_GET_CILIUMNODE=n1
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  if grep -q "^patch ciliumnode" "$KLOG"; then
    echo "BUG: a node was patched after its read failed"
    false
  fi
  rm -rf "$STATE"
}

@test "a CIDR written between the read and the clear makes the patch fail instead of being removed" {
  init_state
  export OPERATOR=none RACE_CIDR=10.244.9.0/24
  add_node n1 10.244.2.0/24 10.244.2.223 10.244.2.135
  rc=0
  "$SCRIPT" > "$STATE/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  [ "$(pod_cidrs_of n1)" = '["10.244.9.0/24"]' ]
  rm -rf "$STATE"
}
