#!/bin/bash
# Moves a cluster installed under ipam.mode kubernetes onto the cluster-pool
# allocation that values-kubeovn.yaml configures. Runs as a post-upgrade hook,
# once the agents and the operator run in cluster-pool mode.
#
# First kube-ovn is told to stay out of Cilium's way, before any agent moves.
# The pool goes into excludeIps of kube-ovn's default subnet, so kube-ovn never
# hands a pod an address Cilium is about to take. A fresh install gets that
# exclusion from --default-exclude-ips, which kube-ovn reads only when it
# creates the subnet. Each node's current Ingress IP goes in too when it lies
# outside the pool. After the move the agent keeps the old Ingress IP in its
# ipcache as reserved:ingress, so a pod must never get it later. The old router
# IP is dropped from the ipcache and goes back to kube-ovn. If kube-ovn has
# already handed out addresses from the pool, the run stops here, before any
# agent moves onto them. It also stops when the pool lies outside that subnet,
# because Cilium's addresses would then sit outside kube-ovn's subnets, where
# Gateway API traffic between nodes breaks, and when the pool has fewer blocks
# than there are nodes.
#
# Then every CiliumNode is moved. cilium-operator gives a CiliumNode a pool
# CIDR only while its spec.ipam.podCIDRs is empty, and every agent that ran in
# ipam.mode kubernetes has copied Node.spec.podCIDR into that field. Left alone,
# the operator records "allocator not configured for the requested CIDR" in the
# CiliumNode status, the spec keeps the old /24, and the agents go on taking
# their router and Ingress IPs from it. Clearing the field any earlier does not
# hold, because an agent still in ipam.mode kubernetes writes it back on its
# next local node update. So for every CiliumNode this clears a CIDR outside
# the pool, waits for the operator to allocate one inside it, and restarts the
# agent when its router or Ingress IP is not inside the node's allocation, one
# node at a time, because an agent reads its allocation only when it starts.
set -euo pipefail
shopt -s inherit_errexit

: "${NAMESPACE:?}" "${POOL:?}" "${MASK:?}"
ALLOCATION_TIMEOUT=${ALLOCATION_TIMEOUT:-180}
AGENT_TIMEOUT=${AGENT_TIMEOUT:-180}
POLL_INTERVAL=${POLL_INTERVAL:-2}

log() {
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
}

ip_to_int() {
  local a b c d
  IFS=. read -r a b c d <<<"$1"
  echo $(( (a << 24) | (b << 16) | (c << 8) | d ))
}

int_to_ip() {
  echo "$(( $1 >> 24 & 255 )).$(( $1 >> 16 & 255 )).$(( $1 >> 8 & 255 )).$(( $1 & 255 ))"
}

# Prints the first and last address of an IPv4 CIDR as integers.
cidr_bounds() {
  local first size
  first=$(ip_to_int "${1%/*}")
  size=$(( 1 << (32 - ${1#*/}) ))
  first=$(( first - first % size ))
  echo "$first $(( first + size - 1 ))"
}

# Prints the bounds of an address or of a range in either form kube-ovn writes,
# first..last in excludeIps and first-last in status.v4usingIPrange.
entry_bounds() {
  local first last
  case $1 in
    *..*) first=${1%%..*} last=${1##*..} ;;
    *-*) first=${1%%-*} last=${1##*-} ;;
    *) first=$1 last=$1 ;;
  esac
  echo "$(ip_to_int "$first") $(ip_to_int "$last")"
}

# contains <cidr> <cidr or address>, IPv4 only.
contains() {
  local outer_first outer_last inner_first inner_last
  read -r outer_first outer_last <<<"$(cidr_bounds "$1")"
  case $2 in
    */*) read -r inner_first inner_last <<<"$(cidr_bounds "$2")" ;;
    *) inner_first=$(ip_to_int "$2") inner_last=$inner_first ;;
  esac
  [ "$inner_first" -ge "$outer_first" ] && [ "$inner_last" -le "$outer_last" ]
}

in_any() {
  local cidrs=$1 value=$2 cidr
  for cidr in $cidrs; do
    if contains "$cidr" "$value"; then
      return 0
    fi
  done
  return 1
}

# covered <excludeIps JSON> <first> <last> succeeds when one entry spans the
# whole first..last range, given as integers.
covered() {
  local entry entry_first entry_last
  for entry in $(jq -r '.[] | select(contains(":") | not)' <<<"$1"); do
    read -r entry_first entry_last <<<"$(entry_bounds "$entry")"
    if [ "$entry_first" -le "$2" ] && [ "$entry_last" -ge "$3" ]; then
      return 0
    fi
  done
  return 1
}

# overlaps <comma-separated ranges> <CIDRs> succeeds when one of the ranges, in
# the form kube-ovn writes its status, overlaps one of the CIDRs.
overlaps() {
  local entry entry_first entry_last cidr first last
  for entry in $(tr ',' ' ' <<<"$1"); do
    read -r entry_first entry_last <<<"$(entry_bounds "$entry")"
    for cidr in $2; do
      read -r first last <<<"$(cidr_bounds "$cidr")"
      if [ "$entry_first" -le "$last" ] && [ "$entry_last" -ge "$first" ]; then
        return 0
      fi
    done
  done
  return 1
}

# check_capacity <CiliumNode list JSON>
# The operator gives every CiliumNode one /MASK block of the pool. A node
# cleared beyond that would wait for a CIDR that never comes, and its agent
# loses pod networking on its next restart, so the run stops before anything
# is changed.
check_capacity() {
  local nodes blocks=0 pool prefix
  nodes=$(jq '.items | length' <<<"$1")
  for pool in $POOL; do
    prefix=${pool#*/}
    if [ "$MASK" -lt "$prefix" ]; then
      log "the /$MASK blocks are wider than the pool $pool. Nothing was changed"
      return 1
    fi
    blocks=$(( blocks + (1 << (MASK - prefix)) ))
  done
  if [ "$nodes" -gt "$blocks" ]; then
    log "there are $nodes CiliumNodes but $POOL holds only $blocks /$MASK blocks. Nothing was changed"
    return 1
  fi
}

# exclude_from_kube_ovn <CiliumNode list JSON>
exclude_from_kube_ovn() {
  local ciliumnodes=$1 attempt subnet name cidr excludes pools pool first last ip add busy deadline current ipobjects ipname node
  if [ -z "$(kubectl get customresourcedefinition subnets.kubeovn.io --ignore-not-found -o name)" ]; then
    log "kube-ovn is not installed, nothing to exclude"
    return 0
  fi
  # kube-ovn rewrites excludeIps into its own canonical form, so a patch can
  # race with it. The test operation turns that into a failed patch, and the
  # subnet is read again.
  for attempt in 1 2 3 4 5; do
    subnet=$(kubectl get subnets.kubeovn.io -o json | jq -c '[.items[] | select(.spec.default == true)] | first // empty')
    if [ -z "$subnet" ]; then
      log "kube-ovn has no default subnet, nothing to exclude"
      return 0
    fi
    name=$(jq -r '.metadata.name' <<<"$subnet")
    cidr=$(jq -r '.spec.cidrBlock | split(",") | map(select(contains(":") | not)) | first // empty' <<<"$subnet")
    excludes=$(jq -c '.spec.excludeIps // []' <<<"$subnet")
    add=()
    pools=''
    for pool in $POOL; do
      if [ -z "$cidr" ] || ! contains "$cidr" "$pool"; then
        log "$pool lies outside subnet $name (${cidr:-no IPv4 block}). networking.podCIDR has to match the cidrBlock of $name, otherwise the agents would take their addresses from outside kube-ovn's subnets. Nothing was changed"
        return 1
      fi
      pools="${pools:+$pools }$pool"
      read -r first last <<<"$(cidr_bounds "$pool")"
      if ! covered "$excludes" "$first" "$last"; then
        add+=("$(int_to_ip "$first")..$(int_to_ip "$last")")
      fi
    done
    for ip in $(jq -r '.items[].spec.ingress.ipv4 // empty' <<<"$ciliumnodes"); do
      if [ -z "$cidr" ] || ! contains "$cidr" "$ip" || in_any "$POOL" "$ip"; then
        continue
      fi
      first=$(ip_to_int "$ip")
      if ! covered "$excludes" "$first" "$first"; then
        add+=("$ip")
      fi
    done
    if [ "${#add[@]}" -eq 0 ]; then
      log "subnet $name already excludes the pool and the current Ingress IPs"
      break
    fi
    log "subnet $name: excluding ${add[*]} from kube-ovn allocation"
    if kubectl patch subnets.kubeovn.io "$name" --type=json \
      -p "[{\"op\":\"test\",\"path\":\"/spec/excludeIps\",\"value\":$excludes},{\"op\":\"replace\",\"path\":\"/spec/excludeIps\",\"value\":$(jq -c '. + $ARGS.positional' --args "${add[@]}" <<<"$excludes")}]"; then
      break
    fi
    if [ "$attempt" -eq 5 ]; then
      log "subnet $name kept changing under the patch, giving up"
      return 1
    fi
    log "subnet $name changed while it was being patched, reading it again"
    sleep "$POLL_INTERVAL"
  done
  busy=$(tr ',' '\n' <<<"$(jq -r '.status.v4usingIPrange // ""' <<<"$subnet")" | while read -r entry; do
    if [ -n "$entry" ] && overlaps "$entry" "$pools"; then
      echo "$entry"
    fi
  done | paste -sd ' ' -)
  if [ -n "$busy" ]; then
    log "subnet $name has already handed out $busy from $pools. Recreate the pods holding them, kube-ovn now places them elsewhere, and the next upgrade attempt moves the agents"
    return 1
  fi
  # kube-ovn applies excludeIps to its allocator asynchronously and then
  # rewrites the subnet status. Once status.v4availableIPrange stops offering
  # the pool, nothing new can land in it. status.v4usingIPrange cannot answer
  # what is still in it, because kube-ovn leaves excluded addresses out of it,
  # so the IP objects it keeps per allocation are read instead. That also
  # covers a run where an earlier attempt already added the exclusion.
  deadline=$(( $(date +%s) + ALLOCATION_TIMEOUT ))
  while :; do
    current=$(kubectl get subnets.kubeovn.io "$name" -o json)
    if ! overlaps "$(jq -r '.status.v4availableIPrange // ""' <<<"$current")" "$pools"; then
      break
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      log "subnet $name still offers addresses from $pools after ${ALLOCATION_TIMEOUT}s, kube-ovn has not applied the exclusion"
      return 1
    fi
    sleep "$POLL_INTERVAL"
  done
  ipobjects=$(kubectl get ips.kubeovn.io -o json \
    | jq -c --arg s "$name" '[.items[] | select(.spec.subnet == $s) | {name: .metadata.name, ip: (.spec.v4IpAddress // "")}]')
  busy=$(jq -r '.[] | "\(.name) \(.ip)"' <<<"$ipobjects" | while read -r ipname ip; do
    if [ -n "$ip" ] && in_any "$pools" "$ip"; then
      echo "$ipname=$ip"
    fi
  done | paste -sd ' ' -)
  if [ -n "$busy" ]; then
    log "subnet $name still has $busy in use inside $pools. Recreate those pods, kube-ovn now places them elsewhere, and the next upgrade attempt moves the agents"
    return 1
  fi
  # A pod that holds a node's current Ingress IP keeps the address after the
  # move, and that node keeps it in its ipcache as reserved:ingress, so the pod
  # stays unreachable from there until it is recreated.
  jq -r --argjson cn "$ciliumnodes" '
    ([$cn.items[] | select(.spec.ingress.ipv4) | {key: .spec.ingress.ipv4, value: .metadata.name}] | from_entries) as $ingress
    | .[] | select($ingress[.ip]) | "\(.name) \(.ip) \($ingress[.ip])"' <<<"$ipobjects" \
    | while read -r ipname ip node; do
      if ! in_any "$POOL" "$ip"; then
        log "$ipname holds $ip, the Ingress IP of $node before the move. Recreate it once the run is done, $node keeps that address as reserved:ingress"
      fi
    done
}

ipv4_pod_cidrs() {
  jq -r '.spec.ipam.podCIDRs // [] | .[] | select(contains(":") | not)'
}

# Prints the node's spec.ipam.podCIDRs as a JSON array when an IPv4 entry lies
# outside the pool, and nothing otherwise.
stale_cidrs() {
  local node cidr
  node=$(kubectl get ciliumnode "$1" -o json)
  for cidr in $(ipv4_pod_cidrs <<<"$node"); do
    if ! in_any "$POOL" "$cidr"; then
      jq -c '.spec.ipam.podCIDRs' <<<"$node"
      return 0
    fi
  done
}

# The test operation turns a value written after it was read, by the operator
# or by an agent, into a failed patch instead of a lost update.
clear_cidrs() {
  log "$1: clearing spec.ipam.podCIDRs $2"
  kubectl patch ciliumnode "$1" --type=json \
    -p "[{\"op\":\"test\",\"path\":\"/spec/ipam/podCIDRs\",\"value\":$2},{\"op\":\"remove\",\"path\":\"/spec/ipam/podCIDRs\"}]"
}

# Prints the node's IPv4 podCIDRs once there are some and all of them lie
# inside the pool.
wait_for_pool_cidrs() {
  local deadline cidrs cidr allocated outside
  deadline=$(( $(date +%s) + ALLOCATION_TIMEOUT ))
  while :; do
    cidrs=$(kubectl get ciliumnode "$1" -o json | ipv4_pod_cidrs) || cidrs=
    allocated=''
    outside=''
    for cidr in $cidrs; do
      if in_any "$POOL" "$cidr"; then
        allocated="${allocated:+$allocated }$cidr"
      else
        outside=1
      fi
    done
    if [ -n "$allocated" ] && [ -z "$outside" ]; then
      echo "$allocated"
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      return 1
    fi
    sleep "$POLL_INTERVAL"
  done
}

# Succeeds when the agent has published a router IP and it, and the Ingress IP
# when there is one, lie inside the given CIDRs. Fails on any other answer,
# including a failed read.
agent_inside() {
  local node router ingress
  node=$(kubectl get ciliumnode "$1" -o json) || return 1
  router=$(jq -r '[.spec.addresses[]? | select(.type == "CiliumInternalIP") | .ip | select(contains(":") | not)] | first // empty' <<<"$node")
  ingress=$(jq -r '.spec.ingress.ipv4 // empty' <<<"$node")
  [ -n "$router" ] || return 1
  in_any "$2" "$router" || return 1
  [ -z "$ingress" ] || in_any "$2" "$ingress"
}

agent_pods() {
  kubectl -n "$NAMESPACE" get pods -l k8s-app=cilium --field-selector "spec.nodeName=$1" -o json \
    | jq -r '.items[] | "\(.metadata.name) \([.status.conditions[]? | select(.type == "Ready") | .status] | first // "Unknown")"'
}

restart_agent() {
  local node=$1 cidrs=$2 ready pods pod deadline
  ready=$(kubectl get node "$node" --ignore-not-found -o json | jq -r '[.status.conditions[]? | select(.type == "Ready") | .status] | first // empty') || return 1
  if [ "$ready" != True ]; then
    log "$node: node is not Ready, its agent takes its addresses from $cidrs when it starts"
    return 0
  fi
  pods=$(agent_pods "$node") || return 1
  pod=$(awk 'NR == 1 { print $1 }' <<<"$pods")
  if [ -z "$pod" ]; then
    log "$node: no agent pod, the next one takes its addresses from $cidrs"
    return 0
  fi
  log "$node: restarting $pod so the agent takes its addresses from $cidrs"
  kubectl -n "$NAMESPACE" delete pod "$pod" --wait=false
  deadline=$(( $(date +%s) + AGENT_TIMEOUT ))
  while :; do
    if agent_pods "$node" | awk -v old="$pod" '$1 != old && $2 == "True" { found = 1 } END { exit !found }' \
      && agent_inside "$node" "$cidrs"; then
      log "$node: agent ready on $cidrs"
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      log "$node: agent not ready on $cidrs after ${AGENT_TIMEOUT}s"
      return 1
    fi
    sleep "$POLL_INTERVAL"
  done
}

ciliumnodes=$(kubectl get ciliumnodes -o json)
check_capacity "$ciliumnodes"
exclude_from_kube_ovn "$ciliumnodes"
failed=0
for node in $(jq -r '.items[].metadata.name' <<<"$ciliumnodes"); do
  stale=$(stale_cidrs "$node")
  if [ -n "$stale" ]; then
    clear_cidrs "$node" "$stale"
  fi
  if ! cidrs=$(wait_for_pool_cidrs "$node"); then
    log "$node: no CIDR from $POOL after ${ALLOCATION_TIMEOUT}s, see status.ipam.operator-status of the CiliumNode and the cilium-operator log"
    failed=1
    continue
  fi
  if agent_inside "$node" "$cidrs"; then
    log "$node: agent on $cidrs"
    continue
  fi
  if ! restart_agent "$node" "$cidrs"; then
    failed=1
  fi
done
exit "$failed"
