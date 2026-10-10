#!/usr/bin/env bash
# The imperative half of hack/e2e-chainsaw/proxmox-network-env/, the suite for
# Proxmox-backed VPC subnets on a real Proxmox VE environment
# (docs/proxmox-networking.md, section 11). The Chainsaw suite calls one phase
# per step, from the repository root:
#
#   hack/e2e-chainsaw/_lib/run-proxmox-network.sh <phase>
#
# Phases, in the order the suite runs them, each able to run on its own
# (state is re-read from the cluster, never carried between invocations):
#
#   preflight   zone, trunk and controller are usable; the trunk NIC is up and
#               steady on every node Kube-OVN reports ready
#   vpc         both tenants' VirtualPrivateCloud applications install and their
#               Proxmox networks become Ready
#   control     VLANs, Vlans, finalizers, the address split, the OVN localnet
#               tag, each network's gateway (its router port's HA chassis
#               group and ARP flows), admission, the egress gateways and their
#               OVN ports
#   datapath    pod-to-pod across subnets, VPCs and tenants, an overlay pod on
#               a node without the trunk, TCP and UDP egress from a pod
#   capi        each tenant namespace reaches its etcd; a Proxmox-backed
#               Kubernetes cluster per tenant joins through the egress gateway;
#               addresses come from the subnet pools; the VM-level isolation
#               matrix with positive controls; UDP egress and the MTU budget
#               from a VM
#   resilience  controller and kube-ovn-cni restarts and a capmox outage change
#               no allocation and keep the trunk carrying every VLAN
#   lifecycle   deleting B's VPC under live VMs holds everything and keeps
#               east-west traffic; deleting B's cluster then releases VLANs,
#               pools and subnets without leftovers
#   teardown    removes tenant A's cluster and VPC
#   diagnostics prints the state the phases read, for a catch block
#
# Each check prints one TAP-style line. A phase exits 1 when any of its checks
# failed and 0 otherwise. "# SKIP" and "# TODO" lines never fail a phase: a
# TODO is an open question or a known limit (docs section 12), reported
# either way, and "ok ... # TODO" means it started to pass.
#
# The environment is described by variables; nothing here names a host, a
# bridge or an address of any particular installation.
#
#   COZY_PXNET_NS_A, COZY_PXNET_NS_B   two tenant namespaces (Tenants with
#                       etcd: true), each holding the Proxmox API credentials
#                       Secret named by COZY_PXNET_CCM_SECRET. Required.
#   COZY_PXNET_ZONE     the ProxmoxNetworkZone; empty takes the only one.
#   COZY_PXNET_TEMPLATE_TAGS  comma-separated tags of the Talos VM template.
#                       Required by the capi phase.
#   COZY_PXNET_NS_NO_NETWORKS, COZY_PXNET_OTHER_BRIDGE  a namespace that owns
#                       no Proxmox networks, and a bridge outside every zone,
#                       for the admission cells about such a namespace; those
#                       cells are skipped when either is unset.
#   COZY_PXNET_LB_RANGE first-last of the LoadBalancer addresses the transit
#                       router forwards to; unset skips that cell.
#   COZY_PXNET_PVE_POOL Proxmox resource pool for the workers (empty: none).
#   COZY_PXNET_ALLOWED_NODES  comma-separated Proxmox nodes capmox may use.
#   COZY_PXNET_ETCD_NS  the control planes' datastore namespace, else each
#                       tenant's own.
#   COZY_PXNET_INTERNET_PROBE, _DNS_PROBE, _NTP_PROBE  IPv4 egress targets;
#                       the datapath probe routes each through its Proxmox
#                       subnet, so a target that is not an address is skipped.
#   COZY_PXNET_DNS_NAME the name the DNS cells resolve.
#   COZY_PXNET_{A,B}_{PRIVATE,DATA,PODS}  the subnets' CIDRs (one /24 each
#                       inside 10.208.0.0/12 by default).
#   COZY_PXNET_CLUSTER_TIMEOUT  seconds a worker may take to join (1800).

set -uo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "$0")/../../.." && pwd)}"
NS_A="${COZY_PXNET_NS_A:-}"
NS_B="${COZY_PXNET_NS_B:-}"
VPC=net
ZONE="${COZY_PXNET_ZONE:-}"
PROVIDER="${COZY_PXNET_PROVIDER:-}"
CONTROLLER_NS="${COZY_PXNET_CONTROLLER_NS:-cozy-proxmox-network}"
CAPMOX_NS="${COZY_PXNET_CAPMOX_NS:-cozy-cluster-api}"
CAPMOX_DEPLOY="${COZY_PXNET_CAPMOX_DEPLOY:-capmox-controller-manager}"
OVN_NS="${COZY_PXNET_OVN_NS:-cozy-kubeovn}"
NETSHOOT="${COZY_PXNET_NETSHOOT:-docker.io/nicolaka/netshoot:v0.16@sha256:b09d9b21381f47a79b3cbcb30da25266dc17186ea00ae65e99fdc51396f48e70}"
KUBECTL_IMAGE="${COZY_PXNET_KUBECTL_IMAGE:-docker.io/alpine/k8s:1.35.2}"
INTERNET_PROBE="${COZY_PXNET_INTERNET_PROBE:-1.1.1.1}"

# Address plan: one /24 per subnet, inside 10.208.0.0/12 by default.
A_PRIVATE="${COZY_PXNET_A_PRIVATE:-10.208.64.0/24}"; A_DATA="${COZY_PXNET_A_DATA:-10.208.65.0/24}"; A_PODS="${COZY_PXNET_A_PODS:-10.208.66.0/24}"
B_PRIVATE="${COZY_PXNET_B_PRIVATE:-10.208.128.0/24}"; B_DATA="${COZY_PXNET_B_DATA:-10.208.129.0/24}"; B_PODS="${COZY_PXNET_B_PODS:-10.208.130.0/24}"

# Cluster parameters. CCM_SECRET must already exist in both tenant namespaces.
K8S_VERSION="${COZY_PXNET_K8S_VERSION:-v1.35}"
ETCD_NS="${COZY_PXNET_ETCD_NS:-}"
TEMPLATE_TAGS="${COZY_PXNET_TEMPLATE_TAGS:-}"
DNS_SERVERS="${COZY_PXNET_DNS_SERVERS:-8.8.8.8,1.1.1.1}"
# UDP egress targets: what a Talos worker needs before it can join. IPv4
# addresses, like INTERNET_PROBE, since the datapath probe gets host routes to
# them.
DNS_PROBE="${COZY_PXNET_DNS_PROBE:-${DNS_SERVERS%%,*}}"
DNS_NAME="${COZY_PXNET_DNS_NAME:-registry.k8s.io}"
NTP_PROBE="${COZY_PXNET_NTP_PROBE:-162.159.200.123}" # time.cloudflare.com, Talos's default
LB_RANGE="${COZY_PXNET_LB_RANGE:-}"
ALLOWED_NODES="${COZY_PXNET_ALLOWED_NODES:-}"
PVE_POOL="${COZY_PXNET_PVE_POOL:-}"
CCM_SECRET="${COZY_PXNET_CCM_SECRET:-proxmox-credentials}"
NS_NO_NETWORKS="${COZY_PXNET_NS_NO_NETWORKS:-}"
OTHER_BRIDGE="${COZY_PXNET_OTHER_BRIDGE:-}"
CLUSTER_TIMEOUT="${COZY_PXNET_CLUSTER_TIMEOUT:-1800}"
ETCD_PROBE_TIMEOUT="${COZY_PXNET_ETCD_PROBE_TIMEOUT:-300}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# --- TAP --------------------------------------------------------------------
n=0; failed=0; skipped=0; todos=0
ok()   { n=$((n+1)); echo "ok $n - $1"; }
no()   { n=$((n+1)); failed=$((failed+1)); echo "not ok $n - $1"; [ $# -lt 2 ] || printf '  # %s\n' "$2"; }
skip() { n=$((n+1)); skipped=$((skipped+1)); echo "ok $n - $1 # SKIP $2"; }
diag() { printf '# %s\n' "$*"; }
check() { if eval "$2" >/dev/null 2>&1; then ok "$1"; else no "$1" "${3:-failed: $2}"; fi; }
# todo DESC CMD REASON: a cell whose outcome is not settled (an open question
# or a known limit). Reported either way, never counted as a failure.
todo() {
  n=$((n+1)); todos=$((todos+1))
  if eval "$2" >/dev/null 2>&1; then echo "ok $n - $1 # TODO $3"; else echo "not ok $n - $1 # TODO $3"; fi
}
# deny: the command must FAIL, and its positive control must have passed.
deny() {
  local desc=$1 cmd=$2 control=${3:-}
  if [ -n "$control" ] && ! eval "$control" >/dev/null 2>&1; then
    no "$desc" "positive control failed, so a refusal here would prove nothing: $control"; return
  fi
  if eval "$cmd" >/dev/null 2>&1; then no "$desc" "expected refusal, got success: $cmd"; else ok "$desc"; fi
}
waitfor() { # waitfor <seconds> <cmd>
  local deadline=$(( $(date +%s) + $1 )); shift
  until eval "$*" >/dev/null 2>&1; do
    [ "$(date +%s)" -ge "$deadline" ] && return 1
    sleep 5
  done
}
# gone KUBECTL-ARGS...: true only when kubectl succeeds and lists nothing. A
# pipeline into grep would not do: with pipefail, `kubectl get A B` exits 1 as
# soon as one of the two is gone, and a negated pipeline then reports both gone.
gone() { local out; out=$(kubectl "$@" --ignore-not-found -o name 2>/dev/null) && [ -z "$out" ]; }

# --- names ------------------------------------------------------------------
sha() { printf '%s' "$1" | sha256sum | cut -c1-"$2"; }
vpc_id()    { echo "vpc-$(sha "$1/virtualprivatecloud-$VPC" 6)"; }
subnet_id() { echo "subnet-$(sha "$1/$(vpc_id "$1")/$2" 8)"; }
first_host() { local net=${1%/*}; echo "${net%.*}.$(( ${net##*.} + 1 ))"; }
host_n()     { local net=${1%/*}; echo "${net%.*}.$2"; }
is_ipv4()    { [[ $1 =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; }

jp() { kubectl get "$@" 2>/dev/null; }
net_field() { jp -n "$1" proxmoxnetwork "$(subnet_id "$1" "$2")" -o "jsonpath=$3"; }

# --- trunk NIC ----------------------------------------------------------------
# on_node NODE CMD...: a read-only command in the ovs-ovn pod of NODE, which
# shares the host's network namespace and the node's OVS database.
on_node() {
  local node=$1 pod; shift
  pod=$(jp -n "$OVN_NS" pods -l app=ovs --field-selector "spec.nodeName=$node" -o jsonpath='{.items[*].metadata.name}')
  [ -n "$pod" ] && kubectl -n "$OVN_NS" exec "${pod%% *}" -c openvswitch -- "$@"
}
trunk_nodes() { jp nodes -l "$PROVIDER.provider-network.kubernetes.io/ready=true" -o jsonpath='{.items[*].metadata.name}'; }
# trunk_if NODE: the provider NIC Kube-OVN bridged on NODE (its node label),
# else the provider network's default interface.
trunk_if() {
  local i; i=$(jp node "$1" -o "jsonpath={.metadata.labels.$PROVIDER\.provider-network\.kubernetes\.io/interface}")
  [ -n "$i" ] || i=$(jp provider-networks.kubeovn.io "$PROVIDER" -o jsonpath='{.spec.defaultInterface}')
  echo "$i"
}
# trunk_up NODE IFACE: the NIC is administratively up and OVS sees its link
# up. Each output is captured first, so a failed exec is a failure.
trunk_up() {
  local flags state
  flags=$(on_node "$1" ip -o link show "$2" 2>/dev/null) || return 1
  state=$(on_node "$1" ovs-vsctl get interface "$2" link_state 2>/dev/null) || return 1
  flags=${flags#*<}; flags=${flags%%>*}
  case ",$flags," in *,UP,*) ;; *) return 1 ;; esac
  [ "$state" = up ]
}
link_resets() { on_node "$1" ovs-vsctl get interface "$2" link_resets 2>/dev/null; }
trunk_state() { # NODE IFACE: the link as the kernel and OVS see it, for a diagnostic
  local flags ovs
  flags=$(on_node "$1" ip -o link show "$2" 2>/dev/null); flags=${flags#*<}; flags=${flags%%>*}
  ovs=$(on_node "$1" ovs-vsctl get interface "$2" admin_state link_state link_resets 2>/dev/null)
  echo "flags <${flags:-?}>, OVS admin_state/link_state/link_resets: ${ovs//$'\n'/ }"
}
# Kube-OVN labels a node ready once the NIC sits in its OVS bridge; the link
# itself can still be down. On Talos this is what a NIC hot-plugged without a
# machine-config entry looks like.
talos_hint() { # NODE IFACE
  echo "likely cause: Talos's default LinkSpec for $2 on $1, which keeps trying to unslave it from ovs-system, fails with 'operation not supported' and leaves the link down; give $2 'ignore: true' in that node's machine.network.interfaces"
}
# trunk_carries NODE IFACE: the trunk port's VLAN list holds the id of every
# Kube-OVN Vlan on the provider network (the transit and each allocated one).
trunk_carries() {
  local want have id
  want=$(kubectl get vlans.kubeovn.io -o "jsonpath={range .items[?(@.spec.provider==\"$PROVIDER\")]}{.spec.id} {end}" 2>/dev/null) || return 1
  have=$(on_node "$1" ovs-vsctl get port "$2" trunks 2>/dev/null) || return 1
  [ -n "$want" ] || return 1
  have=" $(tr -d '[],' <<<"$have") "
  for id in $want; do case "$have" in *" $id "*) ;; *) return 1 ;; esac; done
}

# --- phase: preflight ---------------------------------------------------------
preflight() {
  diag "preflight"
  check "the proxmox.cozystack.io CRDs are installed" \
    "kubectl get crd proxmoxnetworks.proxmox.cozystack.io proxmoxnetworkzones.proxmox.cozystack.io"
  if [ -z "$ZONE" ]; then
    ZONE=$(jp proxmoxnetworkzones -o jsonpath='{.items[0].metadata.name}')
  fi
  check "zone ${ZONE:-<none>} is Ready" \
    "[ \"\$(kubectl get proxmoxnetworkzone $ZONE -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}')\" = True ]" \
    "$(jp proxmoxnetworkzone "$ZONE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}')"
  check "the trunk provider network $PROVIDER is ready on at least one node" \
    "[ -n \"\$(trunk_nodes)\" ]"
  # excludeNodes holds names: one that matches no Node excludes nothing, and
  # the node it meant keeps the provider network from becoming ready.
  # An empty list is valid, so the get itself has to succeed.
  local excl x missing=""
  if excl=$(jp provider-networks.kubeovn.io "$PROVIDER" -o jsonpath='{.spec.excludeNodes[*]}'); then
    for x in $excl; do jp node "$x" -o name >/dev/null || missing="$missing $x"; done
    check "every node in $PROVIDER's excludeNodes exists (${excl:-none})" "[ -z '$missing' ]" "no such Node:$missing"
  else no "every node in $PROVIDER's excludeNodes exists" "cannot read provider network $PROVIDER"; fi
  check "the trunk provider network $PROVIDER is ready on every node it does not exclude" \
    "[ \"\$(kubectl get provider-networks.kubeovn.io $PROVIDER -o jsonpath='{.status.ready}')\" = true ]" \
    "$(jp provider-networks.kubeovn.io "$PROVIDER" -o jsonpath='ready on {.status.readyNodes}, not ready on {.status.notReadyNodes}')"

  # The ready label says the NIC is in the OVS bridge, not that it carries
  # anything: check the link on each such node, then that it does not flap.
  local node ifc r1
  local -A ifcs=() resets0=()
  for node in $(trunk_nodes); do
    ifc=$(trunk_if "$node"); ifcs[$node]=$ifc
    if trunk_up "$node" "$ifc"; then ok "trunk: $ifc on $node is up (admin state and OVS link state)"
    else no "trunk: $ifc on $node is up (admin state and OVS link state)" "$(trunk_state "$node" "$ifc"); $(talos_hint "$node" "$ifc")"; fi
    resets0[$node]=$(link_resets "$node" "$ifc")
  done
  [ ${#ifcs[@]} -eq 0 ] || sleep 20
  for node in "${!ifcs[@]}"; do
    ifc=${ifcs[$node]}; r1=$(link_resets "$node" "$ifc")
    if [ -n "${resets0[$node]}" ] && [ "${resets0[$node]}" = "$r1" ]; then ok "trunk: $ifc on $node does not flap (link_resets steady at $r1 over 20s)"
    else no "trunk: $ifc on $node does not flap" "link_resets ${resets0[$node]:-?} -> ${r1:-?} over 20s; $(talos_hint "$node" "$ifc")"; fi
  done

  check "the controller is available" \
    "kubectl -n $CONTROLLER_NS rollout status deploy/proxmox-network-controller --timeout=60s"
  check "tenant namespaces $NS_A and $NS_B exist" "kubectl get ns $NS_A $NS_B"
  local free; free=$(jp proxmoxnetworkzone "$ZONE" -o jsonpath='{.status.freeVLANs}')
  check "zone $ZONE has at least four free VLANs (found ${free:-0})" "[ ${free:-0} -ge 4 ]"
}

# --- phase: vpc ---------------------------------------------------------------
vpc_values() { # ns private data pods
  cat <<EOF
subnets:
  - name: private
    cidr: "$2"
    proxmox: {zone: "$ZONE"}
  - name: data
    cidr: "$3"
    proxmox: {zone: "$ZONE"}
  - name: pods
    cidr: "$4"
    # The overlay subnet stays private; it admits the VPC's Proxmox subnets.
    allowSubnets: ["$2", "$3"]
egress:
  enabled: true
EOF
}

# install_vpc NS PRIVATE DATA PODS: the tenant's VirtualPrivateCloud, as a
# tenant creates it.
install_vpc() {
  {
    printf 'apiVersion: apps.cozystack.io/v1alpha1\nkind: VirtualPrivateCloud\nmetadata: {name: %s, namespace: %s}\nspec:\n' "$VPC" "$1"
    vpc_values "$@" | sed 's/^/  /'
  } > "$WORK/vpc-$1.yaml"
  kubectl apply -f "$WORK/vpc-$1.yaml" >"$WORK/apply-vpc-$1.log" 2>&1
}

network_ready() { [ "$(net_field "$1" "$2" '{.status.conditions[?(@.type=="Ready")].status}')" = True ]; }

vpc_phase() {
  diag "vpc"
  for t in "A $NS_A $A_PRIVATE $A_DATA $A_PODS" "B $NS_B $B_PRIVATE $B_DATA $B_PODS"; do
    set -- $t
    if install_vpc "$2" "$3" "$4" "$5"; then ok "tenant $1: VirtualPrivateCloud $VPC is accepted"; else no "tenant $1: VirtualPrivateCloud $VPC is accepted" "$(tail -3 "$WORK/apply-vpc-$2.log")"; fi
  done
  for t in "A $NS_A" "B $NS_B"; do
    set -- $t
    for s in private data; do
      if waitfor 300 network_ready "$2" "$s"; then ok "tenant $1: network $s is Ready"
      else no "tenant $1: network $s is Ready" "$(net_field "$2" "$s" '{.status.conditions[?(@.type=="Ready")].reason}: {.status.conditions[?(@.type=="Ready")].message}')"; fi
    done
  done
}

# --- the subnet gateway of a Proxmox network ----------------------------------
# A VM on a Proxmox VLAN reaches its subnet's VPC router port only through the
# switch's localnet port. Kube-OVN builds OVN with its patch 477695a0, which
# drops ARP and ND from a localnet port for the addresses of a router port that
# has neither gateway_chassis nor ha_chassis_group, so the controller gives the
# port (<vpc>-<subnet>) an HA_Chassis_Group px-<port> holding the chassis of the
# zone's trunk nodes (doc section 3). The cells read the NB and SB through
# ovn-central, like the localnet tag cell. Every output is captured before it
# is tested, so a failed exec is a failure, never an absence.
ovn_nb() { kubectl -n "$OVN_NS" exec deploy/ovn-central -c ovn-central -- ovn-nbctl --no-leader-only "$@" 2>/dev/null; }
ovn_sb() { kubectl -n "$OVN_NS" exec deploy/ovn-central -c ovn-central -- ovn-sbctl --no-leader-only "$@" 2>/dev/null; }
gateway_ready() { [ "$(net_field "$1" "$2" '{.status.conditions[?(@.type=="GatewayReady")].status}')" = True ]; }
# router_port ID: Kube-OVN's name for the VPC router port of Subnet ID.
router_port() { local vpc; vpc=$(jp subnets.kubeovn.io "$1" -o jsonpath='{.spec.vpc}') && [ -n "$vpc" ] && echo "$vpc-$1"; }
# trunk_chassis: the OVN chassis (Kube-OVN's node annotation) of every trunk
# node not being deleted, sorted, one per line. The controller leaves out a
# node without one, and so does this.
trunk_chassis() {
  local out; out=$(jp nodes -l "$PROVIDER.provider-network.kubernetes.io/ready=true" -o json) || return 1
  jq -r '.items[] | select(.metadata.deletionTimestamp == null)
    | .metadata.annotations["ovn.kubernetes.io/chassis"] // "" | select(length > 0)' <<<"$out" | sort -u
}
# ha_group LRP: reads the HA_Chassis_Group on router port LRP into hag_name,
# hag_ids (its external_ids, one key=value per line) and hag_chassis (the
# chassis_name of each of its HA_Chassis, sorted, duplicates kept). Fails when
# the port is missing or has no group, or a read fails.
ha_group() {
  local uuid out rows members
  hag_name="" hag_ids="" hag_chassis=""
  uuid=$(ovn_nb get logical_router_port "$1" ha_chassis_group) || return 1
  [ -n "$uuid" ] && [ "$uuid" != "[]" ] || return 1
  # Bare output: one line per column, in the order asked for.
  out=$(ovn_nb --bare --columns=name,external_ids,ha_chassis list ha_chassis_group "$uuid") || return 1
  mapfile -t rows <<<"$out"
  hag_name=${rows[0]:-}
  hag_ids=$(tr ' ' '\n' <<<"${rows[1]:-}")
  read -ra members <<<"${rows[2]:-}"
  [ ${#members[@]} -gt 0 ] || return 0
  out=$(ovn_nb --bare --columns=chassis_name list ha_chassis "${members[@]}") || return 1
  hag_chassis=$(sed '/^$/d' <<<"$out" | sort)
}
# arp_drop_absent LS: the SB's logical flows of switch LS are read, their
# ls_in_arp_rsp stage has flows for the localnet port (the control: the drop
# could be there), and none of that stage's flows is the priority-105 drop of
# patch 477695a0. Sets arp_why when it fails.
arp_drop_absent() {
  local out drop='ls_in_arp_rsp *\), priority=105 *,.*action=\(drop;\)'
  arp_why=""
  if ! out=$(ovn_sb lflow-list "$1"); then arp_why="cannot read the logical flows of switch $1"; return 1; fi
  if ! grep -qE "ls_in_arp_rsp *\), .*inport == \"localnet\.$1\"" <<<"$out"; then
    arp_why="ls_in_arp_rsp of switch $1 has no flow for localnet.$1, so the absence of the drop would prove nothing"; return 1
  fi
  if grep -qE "$drop" <<<"$out"; then
    arp_why="found: $(grep -m2 -E "$drop" <<<"$out" | sed 's/^ *//' | paste -sd ' ' -)"; return 1
  fi
}
# router_gateway_cells TENANT NS SUBNET: the gateway of one Proxmox network, as
# its status, the NB and the SB see it.
router_gateway_cells() {
  local t=$1 ns=$2 s=$3 id lrp want why d_own d_set
  id=$(subnet_id "$ns" "$s")
  if waitfor 60 gateway_ready "$ns" "$s"; then ok "tenant $t/$s: GatewayReady is True"
  else
    why=$(net_field "$ns" "$s" '{.status.conditions[?(@.type=="GatewayReady")].status}/{.status.conditions[?(@.type=="GatewayReady")].reason}: {.status.conditions[?(@.type=="GatewayReady")].message}')
    [ "$why" != "/: " ] || why="the network has no GatewayReady condition: the controller does not manage gateways yet"
    no "tenant $t/$s: GatewayReady is True" "$why"
  fi
  lrp=$(router_port "$id")
  d_own="tenant $t/$s: router port ${lrp:-<unknown>} has HA chassis group px-${lrp:-<unknown>} with external_ids owner=proxmox-network, network=$ns/$id"
  d_set="tenant $t/$s: the group's chassis are exactly the trunk nodes' chassis on $PROVIDER"
  if [ -z "$lrp" ]; then
    why="cannot read spec.vpc of Subnet $id, so its router port is unknown"
    no "$d_own" "$why"; no "$d_set" "$why"
  elif ! waitfor 60 ha_group "$lrp"; then
    why="router port $lrp has no HA_Chassis_Group, or the NB could not be read; without one patch 477695a0 drops the VMs' ARP for their gateway"
    no "$d_own" "$why"; no "$d_set" "$why"
  else
    if [ "$hag_name" = "px-$lrp" ] && grep -qx "owner=proxmox-network" <<<"$hag_ids" && grep -qx "network=$ns/$id" <<<"$hag_ids"; then ok "$d_own"
    else no "$d_own" "the port's group is ${hag_name:-<unnamed>} with external_ids {$(paste -sd, <<<"$hag_ids")}"; fi
    want=$(trunk_chassis)
    if [ -n "$want" ] && [ "$hag_chassis" = "$want" ]; then ok "$d_set"
    else no "$d_set" "group ${hag_name:-<unnamed>} holds {$(paste -sd, <<<"$hag_chassis")}; the trunk nodes' chassis are {$(paste -sd, <<<"$want")}"; fi
  fi
  if waitfor 60 arp_drop_absent "$id"; then ok "tenant $t/$s: ls_in_arp_rsp of switch $id has no priority-105 drop of ARP or ND from its localnet port"
  else no "tenant $t/$s: ls_in_arp_rsp of switch $id has no priority-105 drop of ARP or ND from its localnet port" "$arp_why"; fi
}

# --- phase: control -----------------------------------------------------------
control() {
  diag "control plane objects"
  local vlans=() t ns s id vlan
  for t in "A $NS_A $A_PRIVATE" "B $NS_B $B_PRIVATE"; do
    set -- $t; ns=$2
    for s in private data; do
      id=$(subnet_id "$ns" "$s"); vlan=$(net_field "$ns" "$s" '{.status.vlan}')
      vlans+=("$vlan")
      check "tenant $1/$s: Kube-OVN Vlan $id carries VLAN $vlan on $PROVIDER" \
        "[ \"\$(kubectl get vlans.kubeovn.io $id -o jsonpath='{.spec.id}/{.spec.provider}')\" = '$vlan/$PROVIDER' ]"
      check "tenant $1/$s: the Vlan points back at its network and holds the finalizer" \
        "[ \"\$(kubectl get vlans.kubeovn.io $id -o jsonpath='{.metadata.labels.proxmox\.cozystack\.io/network-namespace}/{.metadata.finalizers}')\" = '$ns/[\"proxmox.cozystack.io/network\"]' ]"
      check "tenant $1/$s: the Subnet is bound to the Vlan, routed by its VPC router, and held by the finalizer" \
        "kubectl get subnets.kubeovn.io $id -o json | jq -e '.spec.vlan == \"$id\" and .spec.logicalGateway == true and (.metadata.finalizers | index(\"proxmox.cozystack.io/network\"))'"
      check "tenant $1/$s: the pool lies inside the subnet's excludeIps (IPPoolReady)" \
        "[ \"\$(kubectl -n $ns get proxmoxnetwork $id -o jsonpath='{.status.conditions[?(@.type==\"IPPoolReady\")].status}')\" = True ]"
      # A failed exec prints nothing, and so does an unread VLAN: both fail.
      check "tenant $1/$s: OVN tags the localnet port with VLAN ${vlan:-<unknown>}" \
        "[ -n '$vlan' ] && [ \"\$(kubectl -n $OVN_NS exec deploy/ovn-central -c ovn-central -- ovn-nbctl --no-leader-only lsp-get-tag localnet.$id)\" = '$vlan' ]"
      router_gateway_cells "$1" "$ns" "$s"
    done
    check "tenant $1: the namespace annotation lists exactly its own networks" \
      "kubectl get ns $ns -o jsonpath='{.metadata.annotations.proxmox\.cozystack\.io/networks}' | tr ',' '\n' | awk -F/ '{print \$3}' | sort | diff - <(printf '%s\n' $(subnet_id "$ns" private) $(subnet_id "$ns" data) | sort)"
  done
  check "the four networks hold four distinct VLANs (${vlans[*]})" \
    "[ \$(printf '%s\n' ${vlans[*]} | sort -u | grep -c .) -eq 4 ]"
  # The pool must exist in A's namespace for its absence from B's to mean
  # anything, and a failed get in B's namespace is not an absence.
  check "tenant B's namespace cannot claim from tenant A's pool (a claim resolves pools in its own namespace only)" \
    "kubectl -n $NS_A get inclusterippool $(subnet_id "$NS_A" private) && gone -n $NS_B get inclusterippool $(subnet_id "$NS_A" private)"
  # Captured, not piped: grep -q closing the pipe early would SIGPIPE kubectl,
  # and pipefail would report that as a failure. Waits for a fresh leader to
  # reconcile after a controller restart.
  exports_metrics() {
    local out; out=$(kubectl get --raw "/api/v1/namespaces/$CONTROLLER_NS/services/proxmox-network-controller-metrics:8080/proxy/metrics" 2>/dev/null)
    grep -q "cozy_proxmox_network_ready{.*namespace=\"$NS_A\"" <<<"$out"
  }
  check "the controller exports zone and network metrics" "waitfor 60 exports_metrics"
  check "a tenant cannot pick a VLAN by hand: the vpc chart has no field for it" \
    "[ -f $REPO_ROOT/packages/apps/vpc/values.schema.json ] && ! grep -q '\"vlan\"' $REPO_ROOT/packages/apps/vpc/values.schema.json"

  # Admission, by server-side dry runs of machine templates.
  local va vb pa pb br
  va=$(net_field "$NS_A" private '{.status.vlan}'); vb=$(net_field "$NS_B" private '{.status.vlan}')
  pa=$(subnet_id "$NS_A" private); pb=$(subnet_id "$NS_B" private)
  br=$(net_field "$NS_A" private '{.status.bridge}')
  # A refusal counts only when it is this policy's: pmt_refused greps the
  # policy name, so a missing namespace or a capmox webhook error is a failure.
  check "admission: tenant A may use its own network" "pmt_dry_run $NS_A $br $va $pa"
  check "admission: tenant A may not use tenant B's VLAN" "pmt_refused $NS_A $br $vb $pb"
  check "admission: tenant A may not pair its pool with B's VLAN" "pmt_refused $NS_A $br $vb $pa"
  # The next cells are about a namespace that owns no Proxmox networks, so
  # that is asserted first, and the refusal counts only next to its positive
  # control in the same namespace: a NIC on a bridge outside every zone.
  local legacy=$NS_NO_NETWORKS other_bridge=$OTHER_BRIDGE d1 d2 d3
  d1="admission: ${legacy:-<unset>} exists and owns no Proxmox networks"
  d2="admission: a namespace without Proxmox networks still uses other bridges"
  d3="admission: a namespace without Proxmox networks may not use the zone bridge"
  if [ -z "$legacy" ] || [ -z "$other_bridge" ]; then
    for d in "$d1" "$d2" "$d3"; do skip "$d" "COZY_PXNET_NS_NO_NETWORKS or COZY_PXNET_OTHER_BRIDGE is unset"; done
  else
    check "$d1" "ns_without_networks $legacy"
    check "$d2" "pmt_dry_run $legacy $other_bridge '' ''"
    if ns_without_networks "$legacy" && pmt_dry_run "$legacy" "$other_bridge" '' ''; then
      check "$d3" "pmt_refused $legacy $br $va $pa"
    else no "$d3" \
      "positive control failed, so a refusal here would prove nothing: $legacy must exist, own no networks and admit a NIC on $other_bridge"; fi
  fi

  # The gateways run in the zone's platform namespace, one per VPC.
  local ns gwns gw pods
  for ns in "$NS_A" "$NS_B"; do
    gw_of "$ns"
    if [ -z "$gw" ]; then no "$ns: its VPC egress gateway exists" "no VpcEgressGateway labelled cozystack.io/tenantName=$ns"; continue; fi
    veg_ready() { [ "$(kubectl -n "$1" get vpc-egress-gateways.kubeovn.io "$2" -o jsonpath='{.status.ready}')" = true ]; }
    check "$ns: its VPC egress gateway is Ready in $gwns" "waitfor 300 veg_ready $gwns $gw"
    diag "$ns: egress gateway $gwns/$gw runs on $(veg_nodes "$gwns" "$gw")"
    pods=$(veg_pods "$gwns" "$gw")
    port_security_not_on() { # NS POD...
      local p v
      for p in "${@:2}"; do v=$(kubectl -n "$1" get pod "$p" -o jsonpath='{.metadata.annotations.ovn\.kubernetes\.io/port_security}') && [ "$v" != true ] || return 1; done
    }
    # The annotation is only the request. Kube-OVN applies it to the logical
    # switch port asynchronously, so the NB is what proves the gateway can
    # forward. A primary interface's port is named <pod>.<namespace>.
    lsp_without_port_security() { # NS POD...
      local p out
      for p in "${@:2}"; do
        out=$(kubectl -n "$OVN_NS" exec deploy/ovn-central -c ovn-central -- ovn-nbctl --no-leader-only lsp-get-port-security "$p.$1" 2>/dev/null) && [ -z "$out" ] || return 1
      done
    }
    check "$ns: the egress gateway pod does not run with port security" "[ -n '$pods' ] && waitfor 120 port_security_not_on $gwns $pods"
    check "$ns: the egress gateway's OVN logical switch port has no port security" "[ -n '$pods' ] && waitfor 120 lsp_without_port_security $gwns $pods"
  done
}

# gw_of NS: sets gwns/gw to the namespace and name of the egress gateway of the
# VPC in tenant namespace NS.
gw_of() {
  local line
  line=$(kubectl get vpc-egress-gateways.kubeovn.io -A -l "cozystack.io/tenantName=$1" -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name}{"\n"}{end}' 2>/dev/null | head -1)
  gwns=${line% *}; gw=${line#* }
  [ -n "$line" ] || { gwns=""; gw=""; }
}
veg_nodes() { jp -n "$1" vpc-egress-gateways.kubeovn.io "$2" -o jsonpath='{.status.workload.nodes[*]}'; }
veg_pods()  { jp -n "$1" pods -l "ovn.kubernetes.io/vpc-egress-gateway=$2" -o jsonpath='{.items[*].metadata.name}'; }

# gw_saw GWNS GW SRC DST: the egress gateway's conntrack holds an entry from
# SRC to DST, so packets between them went through it. Exits 2 when the table
# cannot be read (conntrack in the gateway image carries cap_net_admin).
gw_saw() {
  local p pods out all=""
  pods=$(veg_pods "$1" "$2"); [ -n "$pods" ] || return 2
  for p in $pods; do
    out=$(kubectl -n "$1" exec "$p" -- conntrack -L -s "$3" -d "$4" 2>/dev/null) || return 2
    all+=$out
  done
  [ -n "$all" ]
}
# in_ovn DESC REASON GWNS GW SRC DST CONTROL SENDER...: a TODO cell for the
# VPC boundary inside OVN. A Proxmox subnet's packets to another VPC match no
# allow, so its router reroutes them to the egress gateway, which SNATs them
# onto the transit network, and only the transit router's RFC1918 drop refuses
# them: a deny cell passes either way. The boundary is in OVN only if none of
# SRC's packets to DST reach the gateway GWNS/GW. The control is a ping to
# CONTROL, which leaves the same way and must show up there. SENDER is the
# exec prefix (px NS POD, or vx NS CLUSTER).
in_ovn() {
  local desc=$1 reason=$2 gwns=$3 gw=$4 src=$5 dst=$6 ctl=$7 why="" rc
  shift 7
  if [ -z "$gw" ]; then why="no egress gateway to read"
  elif ! is_ipv4 "$src" || ! is_ipv4 "$dst"; then why="no address to probe (source ${src:-none}, destination ${dst:-none})"
  elif ! is_ipv4 "$ctl"; then why="no control target that leaves through the egress gateway"
  else
    "$@" ping -c2 -W2 "$ctl" >/dev/null 2>&1
    gw_saw "$gwns" "$gw" "$src" "$ctl"; rc=$?
    if [ "$rc" -eq 2 ]; then why="cannot read the conntrack table of $gwns/$gw"
    elif [ "$rc" -ne 0 ]; then why="positive control failed: a ping from $src to $ctl left no conntrack entry in $gwns/$gw, so none for $dst would prove nothing"
    else
      "$@" ping -c2 -W2 "$dst" >/dev/null 2>&1
      gw_saw "$gwns" "$gw" "$src" "$dst"; rc=$?
      if [ "$rc" -eq 2 ]; then why="cannot read the conntrack table of $gwns/$gw"
      elif [ "$rc" -eq 0 ]; then why="packets from $src to $dst went through $gwns/$gw onto the transit network: the transit router's RFC1918 drop refused them, not OVN"; fi
    fi
  fi
  n=$((n+1)); todos=$((todos+1))
  if [ -z "$why" ]; then echo "ok $n - $desc # TODO $reason"
  else echo "not ok $n - $desc # TODO $reason"; printf '  # %s\n' "$why"; fi
}
# Why the A to B deny cells hold today.
OVN_BOUNDARY_TODO="open (doc section 12, question 9): no OVN drop between VPCs yet, only the transit router keeps A out of B"

# ns_without_networks NS: the namespace exists and the controller lists no
# network for it. jsonpath prints nothing for a missing annotation, so the get
# must succeed on its own.
ns_without_networks() {
  local out; out=$(kubectl get ns "$1" -o jsonpath='{.metadata.annotations.proxmox\.cozystack\.io/networks}' 2>/dev/null) && [ -z "$out" ]
}

# pmt_dry_run NS BRIDGE VLAN POOL: would the apiserver admit a machine
# template with that NIC? Nothing is persisted (a CRD kind honours dry-run).
# Empty VLAN and POOL render a raw untagged NIC.
pmt_manifest() {
  cat <<PMT
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxMachineTemplate
metadata: {name: e2e-admission, namespace: $1}
spec:
  template:
    spec:
      sourceNode: none
      templateID: 1
      network:
        default:
          bridge: $2
PMT
  [ -n "$3" ] && echo "          vlan: $3"
  [ -n "$4" ] && echo "          ipv4PoolRef: {apiGroup: ipam.cluster.x-k8s.io, kind: InClusterIPPool, name: $4}"
  return 0
}
pmt_dry_run() { pmt_manifest "$@" | kubectl apply --dry-run=server -f - >/dev/null 2>&1; }
# The refusal makes kubectl exit 1, which under pipefail would fail a pipeline
# into grep even when grep matched, so the output is captured first.
pmt_refused() {
  local out; out=$(pmt_manifest "$@" | kubectl apply --dry-run=server -f - 2>&1)
  grep -q "proxmox-network-machine-binding" <<<"$out"
}

# --- probe pods -------------------------------------------------------------
# A pod with net1 on one VPC subnet, pinned to a trunk node unless a node label
# is given, with routes to the other VPC subnets through that subnet's gateway
# (Kube-OVN's routes annotation, so the pod needs no NET_ADMIN).
probe_pod() { # ns name subnet-name address routes-json [node-label=value]
  local ns=$1 name=$2 id; id=$(subnet_id "$1" "$3")
  local prov="$id.$ns.ovn" sel=${6:-$PROVIDER.provider-network.kubernetes.io/ready=true}
  kubectl -n "$ns" delete pod "$name" --ignore-not-found --wait=true >/dev/null 2>&1
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $name
  namespace: $ns
  labels: {e2e.proxmox.cozystack.io/probe: "true"}
  annotations:
    k8s.v1.cni.cncf.io/networks: $ns/$id
    $prov.kubernetes.io/ip_address: "$4"
    $prov.kubernetes.io/routes: '$5'
spec:
  nodeSelector: {${sel%%=*}: "${sel#*=}"}
  terminationGracePeriodSeconds: 0
  containers:
  - name: probe
    image: $NETSHOOT
    command: [sleep, infinity]
    resources: {requests: {cpu: 5m, memory: 16Mi}, limits: {memory: 64Mi}}
EOF
}
# probe_routes GW DST...: a routes annotation with one route per distinct
# destination through GW.
probe_routes() {
  local gw=$1; shift
  printf '%s\n' "$@" | sort -u | jq -R --arg gw "$gw" '{dst: ., gw: $gw}' | jq -cs .
}
px() { local ns=$1 pod=$2; shift 2; kubectl -n "$ns" exec "$pod" -- "$@"; }
ping_ok() { px "$1" "$2" ping -c2 -W2 "$3"; }
pod_ready() { [ "$(jp -n "$1" pod "$2" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = True ]; }
# far_nodes: the nodes the provider network excludes that an ordinary pod can
# be scheduled on (not cordoned, no NoSchedule or NoExecute taint).
far_nodes() {
  jp nodes -l "$PROVIDER.provider-network.kubernetes.io/exclude=true" -o json |
    jq -r '.items[] | select(.spec.unschedulable != true)
      | select(all(.spec.taints[]?; .effect != "NoSchedule" and .effect != "NoExecute")) | .metadata.name'
}
# egress_check DESC TARGET CMD: an egress cell from e2e-a-private, which has a
# route through its subnet only to a TARGET that is an address. To anything
# else the probe leaves through its primary interface, which proves nothing
# about the egress gateway.
egress_check() {
  if is_ipv4 "$2"; then check "$1" "$3"
  else skip "$1" "$2 is not an IPv4 address, so the probe has no route to it through the subnet"; fi
}

# UDP egress, run through an exec prefix (px NS POD, or vx NS CLUSTER): a DNS
# answer for DNS_NAME from DNS_PROBE, and an SNTP reply from NTP_PROBE (a
# client request only; nothing sets the clock).
dns_ok() {
  local out; out=$("$@" dig +time=3 +tries=2 +short "@$DNS_PROBE" "$DNS_NAME" A 2>/dev/null) &&
    grep -qE '^[0-9]+(\.[0-9]+){3}$' <<<"$out"
}
NTP_QUERY='import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(5)
s.sendto(b"\x1b" + 47 * b"\0", (sys.argv[1], 123))
sys.exit(0 if len(s.recv(512)) >= 48 else 1)'
ntp_ok() { "$@" python3 -c "$NTP_QUERY" "$NTP_PROBE"; }

datapath() {
  diag "datapath between pods"
  local a1 a2 a3 a4 b1 gwa far="" far_label="$PROVIDER.provider-network.kubernetes.io/exclude=true"
  a1=$(host_n "$A_PRIVATE" 10); a2=$(host_n "$A_PODS" 10); a3=$(host_n "$A_PRIVATE" 11); b1=$(host_n "$B_PRIVATE" 10)
  a4=$(host_n "$A_PODS" 11); gwa=$(first_host "$A_PRIVATE")
  # e2e-a-private also routes the egress probes through its subnet, so the
  # VMs' egress path (router reroute, gateway SNAT, transit) is tested before
  # any VM exists. Only addresses: Kube-OVN skips a route it cannot parse, and
  # the probe would then leave through its primary interface instead.
  local dsts=(10.208.0.0/12) e
  for e in "$INTERNET_PROBE" "$DNS_PROBE" "$NTP_PROBE"; do is_ipv4 "$e" && dsts+=("$e/32"); done
  probe_pod "$NS_A" e2e-a-private private "$a1" "$(probe_routes "$gwa" "${dsts[@]}")"
  probe_pod "$NS_A" e2e-a-private2 private "$a3" "[{\"dst\":\"10.208.0.0/12\",\"gw\":\"$gwa\"}]"
  probe_pod "$NS_A" e2e-a-pods pods "$a2" "[{\"dst\":\"10.208.0.0/12\",\"gw\":\"$(first_host "$A_PODS")\"}]"
  probe_pod "$NS_B" e2e-b-private private "$b1" "[{\"dst\":\"10.208.0.0/12\",\"gw\":\"$(first_host "$B_PRIVATE")\"}]"
  # The VPC's overlay subnets are not pinned to trunk nodes, so tenant pods
  # also land on nodes the provider network excludes, wherever those take an
  # ordinary pod. Like a tenant pod, the probe has no tolerations.
  if [ -n "$(far_nodes)" ]; then
    probe_pod "$NS_A" e2e-a-pods-far pods "$a4" "[{\"dst\":\"10.208.0.0/12\",\"gw\":\"$(first_host "$A_PODS")\"}]" "$far_label"
    far=e2e-a-pods-far
  fi
  for p in "$NS_A e2e-a-private" "$NS_A e2e-a-private2" "$NS_A e2e-a-pods" "$NS_B e2e-b-private" ${far:+"$NS_A $far"}; do
    set -- $p
    if kubectl -n "$1" wait --for=condition=Ready "pod/$2" --timeout=180s >/dev/null 2>&1; then ok "probe $2 is running"; else no "probe $2 is running" "$(kubectl -n "$1" describe pod "$2" | tail -5 | tr '\n' ' ')"; fi
  done
  check "A: two pods on the same Proxmox-backed subnet reach each other (L2)" "ping_ok $NS_A e2e-a-private $a3"
  check "A: a pod on the Proxmox subnet reaches the subnet gateway, the VPC router port" "ping_ok $NS_A e2e-a-private $gwa"
  check "A: Proxmox subnet to overlay subnet of the same VPC is routed" "ping_ok $NS_A e2e-a-private $a2"
  if [ -n "$far" ]; then
    diag "$far runs on $(jp -n "$NS_A" pod "$far" -o jsonpath='{.spec.nodeName}'), which $PROVIDER excludes"
    check "A: an overlay pod on a trunk node reaches the overlay pod on a node without the trunk (Geneve)" "ping_ok $NS_A e2e-a-pods $a4"
    # The cell above is the control: the far pod is alive and on the overlay.
    # The reply to a Proxmox subnet is routed into the VLAN switch on the far
    # node, which has no bridge mapping for the provider network.
    todo "A: Proxmox subnet to an overlay pod on a node without the trunk" "ping_ok $NS_A e2e-a-private $a4" \
      "open: the reply enters the VLAN switch on a chassis with no bridge mapping for $PROVIDER"
  else skip "A: Proxmox subnet to an overlay pod on a node without the trunk" "no node labelled $far_label takes an ordinary pod (none, or all cordoned or tainted NoSchedule/NoExecute)"; fi
  deny  "A to B, Proxmox subnet to Proxmox subnet: refused" \
        "ping_ok $NS_A e2e-a-private $b1" "ping_ok $NS_B e2e-b-private $(first_host "$B_PRIVATE")"
  # The deny above proves the transit router, not OVN: see in_ovn. The control
  # is the Internet probe, which e2e-a-private routes through its subnet.
  local gwns gw
  gw_of "$NS_A"
  in_ovn "A to B, Proxmox subnet to Proxmox subnet: refused inside OVN, none of A's packets to B reach A's egress gateway" \
    "$OVN_BOUNDARY_TODO" "$gwns" "$gw" "$a1" "$b1" "$(is_ipv4 "$INTERNET_PROBE" && echo "$INTERNET_PROBE")" px "$NS_A" e2e-a-private
  deny  "B to A: no route back either" \
        "ping_ok $NS_B e2e-b-private $a1" "ping_ok $NS_A e2e-a-private $a3"
  deny  "A cannot resolve B's address at L2 on its own VLAN" \
        "px $NS_A e2e-a-private arping -c2 -w3 -I net1 $b1" "px $NS_A e2e-a-private arping -c2 -w3 -I net1 $a3"

  diag "egress from a pod on a Proxmox subnet"
  egress_check "A: a pod on the Proxmox subnet reaches $INTERNET_PROBE:443 through the egress gateway (TCP)" \
    "$INTERNET_PROBE" "px $NS_A e2e-a-private nc -z -w5 $INTERNET_PROBE 443"
  egress_check "A: a pod on the Proxmox subnet resolves $DNS_NAME at $DNS_PROBE through the egress gateway (UDP 53)" \
    "$DNS_PROBE" "dns_ok px $NS_A e2e-a-private"
  egress_check "A: a pod on the Proxmox subnet gets an NTP reply from $NTP_PROBE through the egress gateway (UDP 123)" \
    "$NTP_PROBE" "ntp_ok px $NS_A e2e-a-private"
}

# --- phase: capi --------------------------------------------------------------
yaml_list() { printf '%s\n' "$1" | tr ',' '\n' | sed "s/^/${2:-}- /"; } # LIST [INDENT]

# cluster_manifest NS CLUSTER WITH-DATA: the tenant's Kubernetes application
# and its worker pool, as a tenant creates them. net0 of every worker is on the
# VPC's "private" subnet; WITH-DATA=yes adds net1 on "data".
cluster_manifest() {
  cat <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata: {name: $2, namespace: $1}
spec:
  version: "$K8S_VERSION"
  substrate: proxmox
  addons:
    certManager: {enabled: false}
    gpuOperator: {enabled: false}
    ingressNginx: {enabled: false}
    monitoringAgents: {enabled: false}
    velero: {enabled: false}
    verticalPodAutoscaler: {enabled: false}
  proxmox:
    insecure: true
    dnsServers:
$(yaml_list "$DNS_SERVERS" "    ")
    allowedNodes:$( [ -n "$ALLOWED_NODES" ] && { echo; yaml_list "$ALLOWED_NODES" "    "; } || echo " []")
    ccm: {credentialsSecretName: $CCM_SECRET}
    csi: {enabled: false}
    network: {vpc: $VPC, subnet: private}
---
apiVersion: apps.cozystack.io/v1alpha1
kind: KubernetesNodes
metadata: {name: $2-md0, namespace: $1}
spec:
  cluster: $2
  version: "$K8S_VERSION"
  instanceType: ""
  minReplicas: 1
  maxReplicas: 3
  resources: {cpu: 2, memory: 4Gi}
  substrate: proxmox
  proxmox:
    templateTags:
$(yaml_list "$TEMPLATE_TAGS" "    ")
    dnsServers:
$(yaml_list "$DNS_SERVERS" "    ")
    pool: "$PVE_POOL"
    network: {vpc: $VPC, subnet: private}
EOF
  if [ "$3" = yes ]; then
    printf '    additionalNetworks:\n    - {name: net1, vpc: %s, subnet: data}\n' "$VPC"
  fi
}

install_cluster() { # ns cluster with-data
  if [ -z "$TEMPLATE_TAGS" ]; then echo "COZY_PXNET_TEMPLATE_TAGS is unset" >"$WORK/apply-k-$2.log"; return 1; fi
  cluster_manifest "$@" > "$WORK/k-$2.yaml"
  kubectl apply -f "$WORK/k-$2.yaml" >"$WORK/apply-k-$2.log" 2>&1
}

nodes_ready() { # ns cluster count
  [ "$(kubectl -n "$1" get machines -l cluster.x-k8s.io/cluster-name="kubernetes-$2" -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' 2>/dev/null | grep -c Running)" -ge "$3" ]
}

# The control planes keep their datastore in ETCD_NS (else in their own
# namespace), and their pods carry policy.cozystack.io/allow-to-etcd=true.
# etcd_probe NS ETCD: a short-lived pod in NS with that label opens TCP 2379
# on etcd.ETCD.svc, as a control-plane pod would. It prints the pod's phase and
# what nc said, and deletes the pod. Exits 0 when nc connected, 1 when it did
# not, 2 when the pod was not created or did not finish. The pod may land on a
# node that has not pulled $NETSHOOT yet, so the deadline (ETCD_PROBE_TIMEOUT)
# covers a pull; nc itself gives up after 5s.
etcd_probe_manifest() { # ns etcd-ns
  cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: e2e-etcd-probe
  namespace: $1
  labels: {e2e.proxmox.cozystack.io/probe: "true", policy.cozystack.io/allow-to-etcd: "true"}
spec:
  restartPolicy: Never
  terminationGracePeriodSeconds: 0
  containers:
  - name: probe
    image: $NETSHOOT
    command: [nc, -z, -v, -w5, etcd.$2.svc, "2379"]
    resources: {requests: {cpu: 5m, memory: 16Mi}, limits: {memory: 64Mi}}
EOF
}
etcd_probe() { # ns etcd-ns
  local phase="" out="" why="" deadline
  kubectl -n "$1" delete pod e2e-etcd-probe --ignore-not-found --wait=true >/dev/null 2>&1
  if ! etcd_probe_manifest "$1" "$2" | kubectl apply -f - >/dev/null 2>&1; then
    echo "cannot create pod $1/e2e-etcd-probe"; return 2
  fi
  deadline=$(( $(date +%s) + ETCD_PROBE_TIMEOUT ))
  until phase=$(jp -n "$1" pod e2e-etcd-probe -o jsonpath='{.status.phase}'); [ "$phase" = Succeeded ] || [ "$phase" = Failed ]; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      # What held it: Unschedulable, ContainerCreating, ImagePullBackOff, ...
      why=$(jp -n "$1" pod e2e-etcd-probe -o jsonpath='{.status.conditions[?(@.type=="PodScheduled")].reason}{.status.containerStatuses[0].state.waiting.reason}')
      why="${why:+ ($why)} after ${ETCD_PROBE_TIMEOUT}s"; break
    fi
    sleep 3
  done
  case $phase in Succeeded|Failed) out=$(kubectl -n "$1" logs e2e-etcd-probe 2>&1 | tail -2 | tr '\n' ' ') ;; esac
  kubectl -n "$1" delete pod e2e-etcd-probe --ignore-not-found --wait=false >/dev/null 2>&1
  echo "probe pod ${phase:-unknown}$why${out:+, nc: ${out% }}"
  case $phase in Succeeded) return 0 ;; Failed) return 1 ;; *) return 2 ;; esac
}
# etcd_cell TENANT NS: the cell for etcd_probe, run before anything waits on a
# join: a control plane that cannot reach its datastore never initialises, no
# worker is ever cloned, and every wait would run out its timeout. Only a
# probe that ran and failed blames the policy.
etcd_cell() {
  local etcd=${ETCD_NS:-$2} res rc desc
  desc="tenant $1: a pod labelled allow-to-etcd in $2 reaches etcd.$etcd.svc:2379"
  res=$(etcd_probe "$2" "$etcd"); rc=$?
  if [ "$rc" -eq 0 ]; then ok "$desc"
  elif [ "$rc" -eq 1 ]; then no "$desc" \
    "$res; the control plane cannot reach its datastore and no worker will join. Check that a network policy in $2 lets pods labelled policy.cozystack.io/allow-to-etcd out to $etcd, and that $etcd admits the ingress"
  else no "$desc" "$res, so the path to etcd is unknown"; fi
}

# A shell inside the management cluster that holds the tenant's admin
# kubeconfig, so the suite can reach tenant apiservers that are only routable
# from inside the cluster.
tenant_shell() { # ns cluster
  local secret="kubernetes-$2-admin-kubeconfig"
  kubectl -n "$1" delete pod "e2e-kshell-$2" --ignore-not-found --wait=true >/dev/null 2>&1
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: {name: e2e-kshell-$2, namespace: $1, labels: {e2e.proxmox.cozystack.io/probe: "true"}}
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - name: k
    image: $KUBECTL_IMAGE
    command: [sleep, infinity]
    env: [{name: KUBECONFIG, value: /k/super-admin.svc}]
    volumeMounts: [{name: k, mountPath: /k, readOnly: true}]
    resources: {requests: {cpu: 5m, memory: 32Mi}, limits: {memory: 128Mi}}
  volumes: [{name: k, secret: {secretName: $secret}}]
EOF
  kubectl -n "$1" wait --for=condition=Ready "pod/e2e-kshell-$2" --timeout=120s >/dev/null 2>&1
}
tk() { local ns=$1 c=$2; shift 2; kubectl -n "$ns" exec "e2e-kshell-$c" -- kubectl "$@"; }

# A host-network probe on the tenant's first worker: what it sends leaves the
# Proxmox VM's own NICs.
vm_probe() { # ns cluster
  cat <<EOF | kubectl -n "$1" exec -i "e2e-kshell-$2" -- kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: {name: vmprobe, namespace: kube-system}
spec:
  hostNetwork: true
  tolerations: [{operator: Exists}]
  terminationGracePeriodSeconds: 0
  containers:
  - name: probe
    image: $NETSHOOT
    command: [sleep, infinity]
    securityContext: {capabilities: {add: [NET_RAW, NET_ADMIN]}}
EOF
  tk "$1" "$2" -n kube-system wait --for=condition=Ready pod/vmprobe --timeout=300s >/dev/null 2>&1
}
# vm_probe_why NS CLUSTER: one line on why vmprobe is not Ready: its phase,
# the waiting reason of its container, its last events and the node's
# taints and Ready condition.
vm_probe_why() {
  local pod ev node
  pod=$(tk "$1" "$2" -n kube-system get pod vmprobe -o jsonpath='{.status.phase} on {.spec.nodeName}; {.status.containerStatuses[0].state.waiting.reason} {.status.containerStatuses[0].state.waiting.message}; scheduled={.status.conditions[?(@.type=="PodScheduled")].status} {.status.conditions[?(@.type=="PodScheduled")].message}' 2>&1)
  ev=$(tk "$1" "$2" -n kube-system get events --field-selector involvedObject.name=vmprobe --sort-by=.lastTimestamp -o jsonpath='{range .items[-3:]}{.reason}: {.message} | {end}' 2>&1)
  node=$(tk "$1" "$2" get nodes -o jsonpath='{range .items[*]}{.metadata.name} ready={.status.conditions[?(@.type=="Ready")].status} taints={.spec.taints[*].key}; {end}' 2>&1)
  printf 'vmprobe: %s | events: %s | nodes: %s' "$pod" "$ev" "$node" | tr '\n' ' ' | cut -c1-900
}
vx() { local ns=$1 c=$2; shift 2; tk "$ns" "$c" -n kube-system exec vmprobe -- "$@"; }
vm_iface() { vx "$1" "$2" ip -o -4 addr show | awk -v ip="$3" '{split($4, a, "/"); if (a[1] == ip) print $2}'; }

# The worker reaches its control plane at the Kamaji LoadBalancer IP, which
# the talos-reconcile job writes into extraHostEntries; the transit router
# forwards only to the range it allows (LB_RANGE, first-last).
kamaji_lb() { jp -n "$1" svc "kubernetes-$2" -o jsonpath='{.status.loadBalancer.ingress[0].ip}'; }
lb_assigned() { [ -n "$(kamaji_lb "$1" "$2")" ]; }
ip2int() { local IFS=.; set -- $1; echo $(( ($1 << 24) + ($2 << 16) + ($3 << 8) + $4 )); }
in_range() { # IP FIRST-LAST
  is_ipv4 "$1" && is_ipv4 "${2%-*}" && is_ipv4 "${2#*-}" || return 1
  local ip; ip=$(ip2int "$1")
  [ "$ip" -ge "$(ip2int "${2%-*}")" ] && [ "$ip" -le "$(ip2int "${2#*-}")" ]
}

# MTU budget. OVN returns no fragmentation-needed and OVS drops a frame larger
# than the port it leaves through, so a size the path cannot carry is lost
# silently: TCP then stalls at the peer's MSS. The cells send DF pings of SIZE
# payload bytes; a destination that does not answer a default-size ping either
# (ICMP filtered on the way) cannot tell a size limit from a filter, so the
# cell is skipped.
df_cell() { # DESC NS CLUSTER DST CMD WHY: CMD decides, WHY explains a failure
  if [ -z "$4" ]; then skip "$1" "no address to probe"; return; fi
  if ! vx "$2" "$3" ping -c2 -W2 "$4" >/dev/null 2>&1; then
    skip "$1" "$4 does not answer a default-size ping"; return
  fi
  check "$1" "$5" "$6"
}
# df_check DESC NS CLUSTER DST SIZE [HINT]: the DF ping must arrive.
df_check() {
  df_cell "$1" "$2" "$3" "$4" "vx $2 $3 ping -c2 -W2 -M do -s $5 $4" "lost at $(( $5 + 28 )) bytes with DF set${6:+; $6}"
}
# df_pmtu DESC NS CLUSTER DST SIZE NIC-MTU [HINT]: the DF ping must arrive, or
# the VM must learn the smaller path MTU (df_signalled). Either way nothing is
# black-holed: that is the acceptance criterion of doc section 3.4.
df_pmtu() {
  df_cell "$1" "$2" "$3" "$4" "df_signalled $2 $3 $4 $5 $6" \
    "lost at $(( $5 + 28 )) bytes with DF set, and no fragmentation-needed came back${7:+; $7}"
}
# df_signalled NS CLUSTER DST SIZE [NIC-MTU]: the DF ping arrives, or the VM
# learns the smaller path MTU: a hop's fragmentation-needed comes back, or the
# VM's kernel refuses the packet itself although it fits the VM's NIC (an
# earlier fragmentation-needed taught it the path MTU). A local refusal of a
# packet larger than the NIC, or with the NIC's MTU unknown, says nothing about
# the path. iputils prints strerror(EMSGSIZE), which is "Message too large"
# with musl (netshoot) and "Message too long" with glibc.
df_signalled() {
  local out; out=$(vx "$1" "$2" ping -c2 -W2 -M 'do' -s "$4" "$3" 2>&1) && return 0
  grep -qi 'frag needed' <<<"$out" && return 0
  [[ ${5:-} =~ ^[0-9]+$ ]] && [ "$5" -ge $(( $4 + 28 )) ] && grep -qiE 'message too (long|large)' <<<"$out"
}
veg_mtu() { # GWNS GW: the MTU of the gateway pod's internal interface
  local p; p=$(veg_pods "$1" "$2")
  [ -n "$p" ] && kubectl -n "$1" exec "${p%% *}" -- cat /sys/class/net/eth0/mtu 2>/dev/null
}
# veg_echo GWNS GW DST SIZE: the egress gateway pod itself sends an echo of
# SIZE payload bytes with DF set. It leaves through net1 from the address VM
# traffic is SNATed to, so it takes the VMs' way past the gateway. The gateway
# image's ping (GNU inetutils) cannot set DF, so python3 sends it on an
# unprivileged ICMP socket with IP_PMTUDISC_DO: a packet the path cannot carry
# is lost, or refused locally once a fragmentation-needed came back. The
# snippet exits 0 on a reply, 4 without one and 3 without an ICMP socket; a
# Python error or a failed exec exits 1, and a missing python3 126 or 127, so
# only 4 means "no reply". veg_echo returns 0 on a reply, 1 without one, and
# 2 when the probe did not run.
ECHO_DF='import socket, struct, sys
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM, socket.IPPROTO_ICMP)
except OSError:
    sys.exit(3)
s.setsockopt(socket.IPPROTO_IP, 10, 2)  # IP_MTU_DISCOVER: IP_PMTUDISC_DO
s.settimeout(2)
for seq in (1, 2):
    try:
        s.sendto(struct.pack("!BBHHH", 8, 0, 0, 0, seq) + bytes(int(sys.argv[2])), (sys.argv[1], 0))
        if s.recv(65535)[0] == 0:
            sys.exit(0)
    except OSError:
        pass
sys.exit(4)'
veg_echo() {
  local p rc; p=$(veg_pods "$1" "$2")
  [ -n "$p" ] || return 2
  kubectl -n "$1" exec "${p%% *}" -- python3 -c "$ECHO_DF" "$3" "$4" >/dev/null 2>&1; rc=$?
  case $rc in 0) return 0 ;; 4) return 1 ;; *) return 2 ;; esac
}

capi() {
  diag "Proxmox-backed clusters on the VPC subnets"
  local ca=pxa cb=pxb
  etcd_cell A "$NS_A"
  etcd_cell B "$NS_B"
  install_cluster "$NS_A" "$ca" yes && ok "tenant A: cluster and two-NIC pool are accepted" || no "tenant A: cluster and two-NIC pool are accepted" "$(tail -3 "$WORK/apply-k-$ca.log")"
  install_cluster "$NS_B" "$cb" no  && ok "tenant B: cluster and pool are accepted" || no "tenant B: cluster and pool are accepted" "$(tail -3 "$WORK/apply-k-$cb.log")"

  local id
  id=$(subnet_id "$NS_A" private)
  sentinel_empty() { [ "$(kubectl -n "$NS_A" get inclusterippool "kubernetes-$ca-v4-icip" -o jsonpath='{.status.ipAddresses.total}' 2>/dev/null)" = 0 ]; }
  check "tenant A: ProxmoxCluster's own pool can allocate nothing (sentinel)" "waitfor 300 sentinel_empty"
  check "tenant A: the machine template uses the subnet's bridge, VLAN and pool for net0 and net1" \
    "kubectl -n $NS_A get proxmoxmachinetemplates -o json | jq -e '[.items[].spec.template.spec.network] | map(select(.default.ipv4PoolRef.name == \"$id\" and .default.vlan == $(net_field "$NS_A" private '{.status.vlan}') and .additionalDevices[0].ipv4PoolRef.name == \"$(subnet_id "$NS_A" data)\")) | length > 0'"

  local lb
  for t in "A $NS_A $ca" "B $NS_B $cb"; do
    set -- $t
    # Before any wait on the worker: an address the transit router does not
    # forward to makes the join impossible.
    if waitfor 300 lb_assigned "$2" "$3"; then
      lb=$(kamaji_lb "$2" "$3"); diag "tenant $1: control-plane LoadBalancer $lb"
      if [ -z "$LB_RANGE" ]; then
        skip "tenant $1: the control-plane LoadBalancer IP lies in the range the transit router forwards to" "LB_RANGE is not set"
      else check "tenant $1: the control-plane LoadBalancer IP $lb lies in LB_RANGE $LB_RANGE, which the transit router forwards to" "in_range $lb $LB_RANGE"; fi
    else no "tenant $1: the control plane gets a LoadBalancer IP" "$(kubectl -n "$2" get svc "kubernetes-$3" 2>&1 | tail -1)"; fi
    if waitfor "$CLUSTER_TIMEOUT" nodes_ready "$2" "$3" 1; then ok "tenant $1: a worker Machine is Running"
    else no "tenant $1: a worker Machine is Running" "$(kubectl -n "$2" get machines,proxmoxmachines 2>&1 | tail -4 | tr '\n' ' ')"; continue; fi
    check "tenant $1: every worker address comes from the subnet pool and is unique" \
      "kubectl -n $2 get ipaddresses.ipam.cluster.x-k8s.io -o json | jq -e '[.items[] | select(.spec.poolRef.name == \"$(subnet_id "$2" private)\") | .spec.address] | (length > 0) and (length == (unique | length))'"
    tenant_shell "$2" "$3" && ok "tenant $1: admin shell reaches the tenant apiserver" || no "tenant $1: admin shell reaches the tenant apiserver"
    if waitfor 600 "tk $2 $3 get nodes -o jsonpath='{.items[*].status.conditions[?(@.type==\"Ready\")].status}' | grep -q True"; then
      ok "tenant $1: the worker joined its control plane through the VPC egress gateway"
    else no "tenant $1: the worker joined its control plane through the VPC egress gateway" "$(tk "$2" "$3" get nodes 2>&1 | tail -2)"; fi
    if vm_probe "$2" "$3"; then ok "tenant $1: host-network probe runs on the Proxmox VM"
    else no "tenant $1: host-network probe runs on the Proxmox VM" "$(vm_probe_why "$2" "$3")"; continue; fi
    # What Talos needs besides its control plane. The probe reaches the VM
    # through the tenant apiserver, so these follow the join; the datapath
    # phase covers the same path from a pod before any VM exists.
    check "tenant $1 VM: resolves $DNS_NAME at $DNS_PROBE through the egress gateway (UDP 53)" "dns_ok vx $2 $3"
    check "tenant $1 VM: gets an NTP reply from $NTP_PROBE through the egress gateway (UDP 123)" "ntp_ok vx $2 $3"
  done

  local avm bvm avm_data
  avm=$(kubectl -n "$NS_A" get ipaddresses.ipam.cluster.x-k8s.io -o json | jq -r "[.items[] | select(.spec.poolRef.name == \"$(subnet_id "$NS_A" private)\") | .spec.address][0]")
  avm_data=$(kubectl -n "$NS_A" get ipaddresses.ipam.cluster.x-k8s.io -o json | jq -r "[.items[] | select(.spec.poolRef.name == \"$(subnet_id "$NS_A" data)\") | .spec.address][0]")
  bvm=$(kubectl -n "$NS_B" get ipaddresses.ipam.cluster.x-k8s.io -o json | jq -r "[.items[] | select(.spec.poolRef.name == \"$(subnet_id "$NS_B" private)\") | .spec.address][0]")
  diag "VM A $avm (data $avm_data), VM B $bvm"
  local aif bif amac
  aif=$(vm_iface "$NS_A" "$ca" "$avm"); bif=$(vm_iface "$NS_B" "$cb" "$bvm")
  amac=$(vx "$NS_A" "$ca" cat "/sys/class/net/$aif/address" 2>/dev/null)

  check "A VM: own gateway, the VPC router port" "vx $NS_A $ca ping -c2 -W2 $(first_host "$A_PRIVATE")"
  check "A VM to A pod on the same subnet (L2 through Proxmox and the trunk)" "vx $NS_A $ca ping -c2 -W2 $(host_n "$A_PRIVATE" 10)"
  check "A VM to A pod on the overlay subnet (routed by VPC A)" "vx $NS_A $ca ping -c2 -W2 $(host_n "$A_PODS" 10)"
  # The cell above, on a trunk node, is the control for the one below.
  if pod_ready "$NS_A" e2e-a-pods-far; then
    todo "A VM to A pod on the overlay subnet on a node without the trunk" "vx $NS_A $ca ping -c2 -W2 $(host_n "$A_PODS" 11)" \
      "predicted to fail: the reply reaches the VLAN switch on a chassis with no bridge mapping for $PROVIDER, where the VM's MAC is unknown"
  else skip "A VM to A pod on the overlay subnet on a node without the trunk" "no overlay probe on a node without the trunk (datapath phase)"; fi
  check "A VM net1 reaches the data subnet gateway (second NIC, second pool)" \
    "vx $NS_A $ca ping -c2 -W2 -I $avm_data $(first_host "$A_DATA")"
  check "A VM reaches the Internet through the egress gateway" "vx $NS_A $ca nc -z -w5 $INTERNET_PROBE 443"

  # MTU budget: a VM sends packets up to its MTU, and every egress hop must
  # carry that or tell the VM (fragmentation-needed). With a zone mtu the chart
  # writes it into every VM NIC, and that NIC is checked below; without one the
  # VM's MTU is its NIC's, the bridge's 1500 by default. Overlay pods have
  # Kube-OVN's smaller MTU.
  local gwns gw vmtu nmtu mtu src full gwmtu tgw pmtu small opod
  gw_of "$NS_A"
  nmtu=$(net_field "$NS_A" private '{.status.mtu}')
  [[ $nmtu =~ ^[0-9]+$ ]] || nmtu=""
  vmtu=$(vx "$NS_A" "$ca" cat "/sys/class/net/$aif/mtu" 2>/dev/null)
  [[ $vmtu =~ ^[0-9]+$ ]] || vmtu=""
  if [ -n "$nmtu" ]; then mtu=$nmtu src="its network's"
  elif [ -n "$vmtu" ]; then mtu=$vmtu src="its NIC's; the network sets none"
  else mtu=1500 src="the bridge's default; the network sets none and the NIC's is unread"; fi
  full=$(( mtu - 28 ))
  gwmtu=$( [ -n "$gw" ] && veg_mtu "$gwns" "$gw")
  tgw=$( [ -n "$gw" ] && jp subnets.kubeovn.io "$(jp -n "$gwns" vpc-egress-gateways.kubeovn.io "$gw" -o jsonpath='{.spec.externalSubnet}')" -o jsonpath='{.spec.gateway}')
  # The overlay probe's attachment MTU, else Kube-OVN's default with Geneve.
  opod=$(host_n "$A_PODS" 10)
  pmtu=$(px "$NS_A" e2e-a-pods cat /sys/class/net/net1/mtu 2>/dev/null)
  [[ $pmtu =~ ^[0-9]+$ ]] || pmtu=1400
  small=$(( mtu < pmtu ? mtu : pmtu ))
  diag "A VM MTU $mtu ($src); A's network MTU ${nmtu:-unset}; A VM NIC ${aif:-?} MTU ${vmtu:-?}; A's egress gateway internal MTU ${gwmtu:-?}; overlay pod MTU $pmtu; transit router ${tgw:-?}"
  # A zone MTU must reach the VM, or the VM sends larger packets than the
  # cells below test.
  if [ -n "$nmtu" ]; then
    check "A VM: its NIC ${aif:-?} has its network's MTU $nmtu" "[ '$vmtu' = '$nmtu' ]" "the NIC has MTU ${vmtu:-?}: the zone's mtu did not reach the VM"
  fi
  # Who to blame when a full-size packet is lost: a NIC below the network's
  # MTU refuses it in the VM itself, else the egress gateway's eth0 is the hop
  # that can be smaller (doc section 3.4).
  local nic_hint="" mtu_hint
  if [ -n "$vmtu" ] && [ "$vmtu" -lt "$mtu" ]; then
    nic_hint="the VM's NIC ${aif:-?} carries only $vmtu of its network's $mtu, so the VM refuses the packet itself (see the NIC cell)"
  fi
  mtu_hint=${nic_hint:-"the egress gateway's internal interface has MTU ${gwmtu:-?} against the VM's $mtu"}
  # The egress cells pass when the packet arrives or the VM learns the smaller
  # path MTU (df_pmtu): a silent loss is the black hole.
  # An Internet target also crosses the uplink past the transit router, whose
  # path MTU can be smaller and drop a larger DF packet without
  # fragmentation-needed. The control is A's
  # egress gateway sending a DF packet of the same size itself: when that gets
  # no reply, the limit lies past the gateway, outside this design, and the
  # cell is skipped; the transit router cell below carries the check then.
  local inet="A VM: a DF packet of its MTU ($mtu bytes) to $INTERNET_PROBE through the egress gateway arrives, or the VM learns the smaller path MTU" rc
  if [ -z "$gw" ]; then skip "$inet" "tenant A has no egress gateway to run the uplink control from"
  elif ! is_ipv4 "$INTERNET_PROBE"; then skip "$inet" "$INTERNET_PROBE is not an IPv4 address, which the uplink control in the egress gateway needs"
  else
    veg_echo "$gwns" "$gw" "$INTERNET_PROBE" 56; rc=$?
    if [ "$rc" -gt 1 ]; then skip "$inet" "the uplink control did not run in $gwns/$gw (it needs kubectl exec, python3 and an unprivileged ICMP socket)"
    elif [ "$rc" -eq 1 ]; then skip "$inet" "A's egress gateway itself gets no echo reply from $INTERNET_PROBE"
    else
      veg_echo "$gwns" "$gw" "$INTERNET_PROBE" "$full"; rc=$?
      if [ "$rc" -gt 1 ]; then skip "$inet" "the uplink control did not run in $gwns/$gw at $mtu bytes"
      elif [ "$rc" -eq 1 ]; then
        skip "$inet" "the uplink does not carry $mtu bytes: A's egress gateway's own DF packet of that size to $INTERNET_PROBE gets no reply past the gateway"
      else df_pmtu "$inet" "$NS_A" "$ca" "$INTERNET_PROBE" "$full" "$vmtu" "$mtu_hint"; fi
    fi
  fi
  df_pmtu "A VM: a DF packet of its MTU ($mtu bytes) to its control-plane LoadBalancer arrives, or the VM learns the smaller path MTU" \
    "$NS_A" "$ca" "$(kamaji_lb "$NS_A" "$ca")" "$full" "$vmtu" "$mtu_hint"
  # The acceptance cell of doc section 3.4. The transit router answers ICMP
  # where the LoadBalancer range may not, and sits on the same egress path up
  # to the LoadBalancer's network.
  df_pmtu "A VM: a DF packet of its MTU ($mtu bytes) to the transit router through the egress gateway arrives, or the VM learns the smaller path MTU" \
    "$NS_A" "$ca" "$tgw" "$full" "$vmtu" "$mtu_hint"
  # A size every hop carries must arrive.
  df_check "A VM: a DF packet of $small bytes, the smaller of its MTU and the overlay pod's, reaches the overlay pod" "$NS_A" "$ca" "$opod" $(( small - 28 )) "$nic_hint"
  local big="A VM: a DF packet of its MTU ($mtu bytes) to the overlay pod arrives, or the VM learns the smaller path MTU"
  if [ "$mtu" -gt "$pmtu" ]; then
    todo "$big" "df_signalled $NS_A $ca $opod $full $vmtu" \
      "known limit: VMs at MTU $mtu next to overlay pods at $pmtu, and OVN returns no fragmentation-needed"
  else skip "$big" "its MTU $mtu fits the overlay pod's $pmtu"; fi
  deny "A VM to B VM, L3" "vx $NS_A $ca ping -c2 -W2 $bvm" "vx $NS_B $cb ping -c2 -W2 $(first_host "$B_PRIVATE")"
  # The deny above proves the transit router, not OVN: see in_ovn. The control
  # is the transit router, else the Internet probe.
  local ctl=$tgw
  [ -n "$ctl" ] || { is_ipv4 "$INTERNET_PROBE" && ctl=$INTERNET_PROBE; }
  in_ovn "A VM to B VM, L3: refused inside OVN, none of A's packets to B reach A's egress gateway" \
    "$OVN_BOUNDARY_TODO" "$gwns" "$gw" "$avm" "$bvm" "$ctl" vx "$NS_A" "$ca"
  deny "A VM to B VM, L2 (ARP on A's VLAN)" "vx $NS_A $ca arping -c2 -w3 -I $aif $bvm" "vx $NS_A $ca arping -c2 -w3 -I $aif $(host_n "$A_PRIVATE" 10)"
  # Broadcast: a listener captures frames from A's MAC while A sends ARP
  # requests, which are broadcast. The positive control is a pod on A's own
  # subnet, where the frames must arrive; the check is B's VM.
  saw_broadcast() { # pod|vm ns name-or-cluster iface
    local out
    ( sleep 3; vx "$NS_A" "$ca" arping -c3 -w4 -I "$aif" "$(host_n "$A_PRIVATE" 254)" >/dev/null 2>&1 ) &
    if [ "$1" = pod ]; then out=$(px "$2" "$3" timeout 12 tcpdump -p -n -c1 -i "$4" "ether src $amac" 2>/dev/null)
    else out=$(vx "$2" "$3" timeout 12 tcpdump -p -n -c1 -i "$4" "ether src $amac" 2>/dev/null); fi
    wait
    [ -n "$out" ]
  }
  # The control proves both ends: A's broadcast reaches a listener on A's own
  # subnet, and B's listener is alive and captures a frame it must see (its
  # own pings to its gateway).
  b_listens() {
    [ -n "$bif" ] || return 1
    ( sleep 3; vx "$NS_B" "$cb" ping -c3 -W1 "$(first_host "$B_PRIVATE")" >/dev/null 2>&1 ) &
    local out; out=$(vx "$NS_B" "$cb" timeout 12 tcpdump -p -n -c1 -i "$bif" icmp 2>/dev/null)
    wait
    [ -n "$out" ]
  }
  deny "A VM broadcast does not reach B VM" "saw_broadcast vm $NS_B $cb $bif" "b_listens && saw_broadcast pod $NS_A e2e-a-private net1"
  # Open question 4 is about the transit ACL between gateways on different
  # chassis; the scheduler may put both on one node, and nothing here moves
  # them, so the placement is recorded and the cross-chassis cell skipped then.
  local begw anode bnode
  gw_of "$NS_A"; anode=$( [ -n "$gw" ] && veg_nodes "$gwns" "$gw")
  gw_of "$NS_B"; bnode=$( [ -n "$gw" ] && veg_nodes "$gwns" "$gw")
  begw=$( [ -n "$gw" ] && kubectl -n "$gwns" get vpc-egress-gateways.kubeovn.io "$gw" -o jsonpath='{.status.externalIPs[0]}' 2>/dev/null)
  diag "egress gateways: A's on ${anode:-?}, B's on ${bnode:-?}"
  if [ -z "$begw" ]; then
    skip "A VM cannot reach B's egress gateway on the transit network" "B's egress gateway reports no external IP"
  else
    deny "A VM cannot reach B's egress gateway on the transit network (A's gateway on ${anode:-?}, B's on ${bnode:-?})" \
      "vx $NS_A $ca ping -c2 -W2 ${begw%/*}" "vx $NS_A $ca nc -z -w5 $INTERNET_PROBE 443"
    if [ -z "$anode" ] || [ -z "$bnode" ]; then
      skip "the transit ACL holds between gateways on different chassis (open question 4)" "the gateways' nodes are unknown"
    elif [ "$anode" = "$bnode" ]; then
      skip "the transit ACL holds between gateways on different chassis (open question 4)" "both gateways run on $anode, so the cell above covered one chassis only"
    fi
  fi
}

# --- phase: resilience ---------------------------------------------------------
snapshot_vlans() { kubectl get vlans.kubeovn.io -l proxmox.cozystack.io/zone="$ZONE" -o jsonpath='{range .items[*]}{.metadata.name}={.spec.id}{"\n"}{end}' | sort; }
snapshot_machines() { kubectl -n "$1" get proxmoxmachines -o jsonpath='{range .items[*]}{.metadata.uid} {.spec.providerID} {.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}' | sort; }

resilience() {
  diag "resilience"
  local before after
  before=$(snapshot_vlans)
  kubectl -n "$CONTROLLER_NS" delete pod -l app=proxmox-network-controller --wait=true >/dev/null 2>&1
  check "the controller comes back" "kubectl -n $CONTROLLER_NS rollout status deploy/proxmox-network-controller --timeout=120s"
  sleep 30
  after=$(snapshot_vlans)
  check "a controller restart allocates nothing and moves nothing" "[ -n \"\$before\" ] && [ \"\$before\" = \"\$after\" ]" "before: $before | after: $after"
  all_ready() {
    local out; out=$(kubectl get proxmoxnetworks -A -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}') || return 1
    [ -n "$out" ] && ! printf '%s\n' "$out" | grep -qv '^True$'
  }
  check "every network is still Ready after the restart" "all_ready"

  # kube-ovn-cni owns the trunk port: after a restart it must bring the NIC up
  # and rebuild the port's VLAN list from the provider network, and traffic
  # through the trunk must follow. The node is the one A's probe pod runs on.
  local tnode cni ifc
  tnode=$(jp -n "$NS_A" pod e2e-a-private -o jsonpath='{.spec.nodeName}')
  [ -n "$tnode" ] || { tnode=$(trunk_nodes); tnode=${tnode%% *}; }
  ifc=$( [ -n "$tnode" ] && trunk_if "$tnode")
  cni=$( [ -n "$tnode" ] && jp -n "$OVN_NS" pods -l app=kube-ovn-cni --field-selector "spec.nodeName=$tnode" -o jsonpath='{.items[*].metadata.name}')
  if [ -z "$cni" ]; then
    no "kube-ovn-cni restarts on a trunk node" "no kube-ovn-cni pod on ${tnode:-any trunk node}"
  else
    check "before the restart the trunk port $ifc on $tnode carries the transit and every allocated VLAN" "trunk_carries $tnode $ifc"
    kubectl -n "$OVN_NS" delete pod "${cni%% *}" --wait=true >/dev/null 2>&1
    check "kube-ovn-cni on $tnode comes back" "kubectl -n $OVN_NS rollout status ds/kube-ovn-cni --timeout=180s"
    check "after the restart $ifc on $tnode is up" "waitfor 60 trunk_up $tnode $ifc" "$(talos_hint "$tnode" "$ifc")"
    # kube-ovn-cni sets the link up on start; something else on the node may
    # take it down again moments later, so it has to stay up.
    local r0; r0=$(link_resets "$tnode" "$ifc"); sleep 20
    check "after the restart $ifc on $tnode stays up (link_resets steady over 20s)" \
      "[ -n '$r0' ] && [ \"\$(link_resets $tnode $ifc)\" = '$r0' ] && trunk_up $tnode $ifc" "$(talos_hint "$tnode" "$ifc")"
    check "after the restart the trunk port $ifc on $tnode carries the transit and every allocated VLAN" "waitfor 120 trunk_carries $tnode $ifc"
    egress_check "after the restart A's probe pod on $tnode still reaches the Internet through the egress gateway" \
      "$INTERNET_PROBE" "waitfor 60 px $NS_A e2e-a-private nc -z -w5 $INTERNET_PROBE 443"
    if vx "$NS_A" pxa true >/dev/null 2>&1; then
      check "after the restart A's VM reaches a pod on its subnet through the trunk" \
        "waitfor 60 vx $NS_A pxa ping -c2 -W2 $(host_n "$A_PRIVATE" 10)"
    else skip "after the restart A's VM reaches a pod on its subnet through the trunk" "no host-network probe on A's VM (capi phase)"; fi
  fi

  # A capmox outage while the pool grows: the new machine waits, then gets an
  # address nobody else holds, and the old machines are left alone.
  local md; md=$(kubectl -n "$NS_A" get machinedeployments -l cluster.x-k8s.io/cluster-name=kubernetes-pxa -o jsonpath='{.items[0].metadata.name}')
  local old
  # shellcheck disable=SC2034 # read inside the eval of a check below
  old=$(snapshot_machines "$NS_A")
  local replicas; replicas=$(kubectl -n "$NS_A" get md "$md" -o jsonpath='{.spec.replicas}')
  kubectl -n "$CAPMOX_NS" scale deploy "$CAPMOX_DEPLOY" --replicas=0 >/dev/null
  kubectl -n "$NS_A" scale md "$md" --replicas=$((replicas+1)) >/dev/null
  sleep 60
  kubectl -n "$CAPMOX_NS" scale deploy "$CAPMOX_DEPLOY" --replicas=1 >/dev/null
  if waitfor "$CLUSTER_TIMEOUT" nodes_ready "$NS_A" pxa $((replicas+1)); then ok "after the capmox outage the extra worker is provisioned"
  else no "after the capmox outage the extra worker is provisioned"; fi
  check "no address is held twice in tenant A's pool" \
    "kubectl -n $NS_A get ipaddresses.ipam.cluster.x-k8s.io -o json | jq -e '[.items[] | select(.spec.poolRef.name == \"$(subnet_id "$NS_A" private)\") | .spec.address] | length == (unique | length)'"
  check "the machines that existed before the outage were not recreated" \
    "[ -n \"\$old\" ] && [ -z \"\$(comm -23 <(printf '%s\\n' \"\$old\") <(snapshot_machines $NS_A))\" ]"
  kubectl -n "$NS_A" scale md "$md" --replicas="$replicas" >/dev/null
}

# --- phase: lifecycle ----------------------------------------------------------
lifecycle() {
  diag "lifecycle"
  local idp idd vlanp free0 bvm gwns gw bgwns bgw ew0=no
  idp=$(subnet_id "$NS_B" private); idd=$(subnet_id "$NS_B" data); vlanp=$(net_field "$NS_B" private '{.status.vlan}')
  # Baseline measured here, not in preflight: the phases may run in separate
  # invocations.
  free0=$(kubectl get proxmoxnetworkzone "$ZONE" -o jsonpath='{.status.freeVLANs}' 2>/dev/null)
  # The uninstall also removes B's egress gateway, which the VM's konnectivity
  # session (and so vx) rides on. What survives is checked east-west only:
  # B's probe pod and B's VM share a VLAN, and that path needs neither.
  bvm=$(kubectl -n "$NS_B" get ipaddresses.ipam.cluster.x-k8s.io -o json 2>/dev/null | jq -r "[.items[] | select(.spec.poolRef.name == \"$idp\") | .spec.address][0] // empty")
  gw_of "$NS_B"; bgwns=$gwns; bgw=$gw
  b_east_west() { [ -n "$bvm" ] && ping_ok "$NS_B" e2e-b-private "$bvm"; }
  if b_east_west; then ew0=yes; ok "B: before the uninstall B's probe pod reaches B's VM ${bvm} on their VLAN"
  else no "B: before the uninstall B's probe pod reaches B's VM ${bvm:-<none>} on their VLAN"; fi
  local uwhy
  if uwhy=$(uninstall_released "$NS_B"); then ok "B: the VPC release uninstalls while B's VM holds addresses"
  else no "B: the VPC release uninstalls while B's VM holds addresses" "$uwhy"; fi
  sleep 20
  check "B: removing the VPC under live VMs leaves the network Terminating with reason InUse" \
    "[ \"\$(kubectl -n $NS_B get proxmoxnetwork $idp -o jsonpath='{.metadata.deletionTimestamp}/{.status.conditions[?(@.type==\"Ready\")].reason}' | cut -d/ -f2)\" = InUse ]"
  check "B: VLAN $vlanp is not released while VMs hold addresses" "kubectl get vlans.kubeovn.io $idp"
  check "B: the Subnet is kept" "kubectl get subnets.kubeovn.io $idp"
  if [ "$ew0" = yes ]; then check "B: east-west traffic survives: B's probe pod still reaches B's VM on their VLAN" "b_east_west"
  else skip "B: east-west traffic survives: B's probe pod still reaches B's VM on their VLAN" "the baseline before the uninstall failed"; fi
  # Kube-OVN drops a Vpc, and its router, once every Subnet left in it is
  # terminating (getVpcSubnets skips those), so the gateway may go first.
  todo "B: B's probe pod still reaches the subnet gateway, the VPC router port" "ping_ok $NS_B e2e-b-private $(first_host "$B_PRIVATE")" \
    "open: the router may be deleted with the Vpc while the held Subnets stay"
  b_egress_gone() { gone -n "$bgwns" get vpc-egress-gateways.kubeovn.io "$bgw" && gone -n "$bgwns" get pods -l "ovn.kubernetes.io/vpc-egress-gateway=$bgw"; }
  if [ -n "$bgw" ]; then
    check "B: the egress gateway goes with the release (B's VMs lose egress and their control-plane connection)" "waitfor 120 b_egress_gone"
  else skip "B: the egress gateway goes with the release" "B had no egress gateway before the uninstall"; fi

  # Kube-OVN keeps a Subnet while any pod IP is used in it, so B's probe pod
  # goes together with the cluster.
  kubectl -n "$NS_B" delete pod -l e2e.proxmox.cozystack.io/probe=true --ignore-not-found --wait=false >/dev/null 2>&1
  kubectl -n "$NS_B" delete kubernetesnodes.apps.cozystack.io pxb-md0 --ignore-not-found --wait=false >/dev/null 2>&1
  kubectl -n "$NS_B" delete kubernetes.apps.cozystack.io pxb --ignore-not-found --wait=false >/dev/null 2>&1
  if waitfor "$CLUSTER_TIMEOUT" gone -n "$NS_B" get proxmoxnetwork "$idp" "$idd"; then
    ok "B: once the machines are gone the networks finish deleting"
  else no "B: once the machines are gone the networks finish deleting" "$(kubectl -n "$NS_B" get proxmoxnetworks -o wide 2>&1 | tail -3)"; fi
  check "B: Vlans released" "waitfor 120 gone get vlans.kubeovn.io $idp $idd"
  subnets_gone() { gone get subnets.kubeovn.io "$idp" "$idd"; }
  pools_gone()   { gone -n "$NS_B" get inclusterippools.ipam.cluster.x-k8s.io "$idp" "$idd"; }
  zone_freed()   { local f; f=$(kubectl get proxmoxnetworkzone "$ZONE" -o jsonpath='{.status.freeVLANs}'); [ -n "$free0" ] && [ "${f:-0}" -ge $(( free0 + 2 )) ]; }
  check "B: Subnets gone" "waitfor 120 subnets_gone"
  check "B: pools gone" "waitfor 120 pools_gone"
  check "B: no address left behind" "gone -n $NS_B get ipaddresses.ipam.cluster.x-k8s.io"
  check "B: the namespace annotation is cleared" "ns_without_networks $NS_B"
  check "zone: B's two VLANs are free again" "waitfor 120 zone_freed"
}

# uninstall_released NS: deletes the namespace's VirtualPrivateCloud and
# waits for its HelmRelease to go, reporting what holds it otherwise. A
# release left uninstalling keeps whatever helm could not delete, e.g. a pool
# the IPAM provider's delete webhook refused.
uninstall_released() {
  local out st hr="virtualprivatecloud-$VPC"
  out=$(kubectl -n "$1" delete virtualprivateclouds.apps.cozystack.io "$VPC" --ignore-not-found --wait=false 2>&1) || true
  if waitfor 180 gone -n "$1" get helmreleases.helm.toolkit.fluxcd.io "$hr"; then return 0; fi
  st=$(kubectl -n "$1" get helmreleases.helm.toolkit.fluxcd.io "$hr" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}: {.status.conditions[?(@.type=="Ready")].message}' 2>/dev/null)
  printf '%s | delete: %s' "${st:-still present}" "$out" | tr '\n' ' ' | cut -c1-600
  return 1
}

teardown() {
  diag "teardown"
  kubectl -n "$NS_A" delete pod -l e2e.proxmox.cozystack.io/probe=true --wait=false >/dev/null 2>&1
  kubectl -n "$NS_B" delete pod -l e2e.proxmox.cozystack.io/probe=true --wait=false >/dev/null 2>&1
  kubectl -n "$NS_A" delete kubernetesnodes.apps.cozystack.io pxa-md0 --ignore-not-found --wait=false >/dev/null 2>&1
  kubectl -n "$NS_A" delete kubernetes.apps.cozystack.io pxa --ignore-not-found --wait=false >/dev/null 2>&1
  local why
  if why=$(uninstall_released "$NS_A"); then ok "A: the VPC release uninstalls while its VM may still hold addresses"
  else no "A: the VPC release uninstalls while its VM may still hold addresses" "$why"; fi
  if waitfor "$CLUSTER_TIMEOUT" gone -n "$NS_A" get proxmoxnetworks; then ok "A: everything released"
  else no "A: everything released" "$(kubectl -n "$NS_A" get proxmoxnetworks -o wide 2>&1 | tail -3)"; fi
  check "no zone Vlan is left behind" "waitfor 300 gone get vlans.kubeovn.io -l proxmox.cozystack.io/zone=$ZONE"
}

diagnostics() {
  local ns
  diag "zones";    kubectl get proxmoxnetworkzones -o wide 2>&1
  diag "networks"; kubectl get proxmoxnetworks -A -o wide 2>&1
  for ns in $NS_A $NS_B; do
    diag "$ns: applications and releases"
    kubectl -n "$ns" get virtualprivateclouds.apps.cozystack.io,kubernetes.apps.cozystack.io,kubernetesnodes.apps.cozystack.io,helmreleases.helm.toolkit.fluxcd.io 2>&1
    diag "$ns: machines and addresses"
    kubectl -n "$ns" get machines.cluster.x-k8s.io,proxmoxmachines,ipaddresses.ipam.cluster.x-k8s.io 2>&1
    diag "$ns: probe pods"
    kubectl -n "$ns" get pods -l e2e.proxmox.cozystack.io/probe=true -o wide 2>&1
  done
  diag "provider network"
  [ -z "$PROVIDER" ] || kubectl get provider-networks.kubeovn.io "$PROVIDER" -o yaml 2>&1
  diag "egress gateways"
  kubectl get vpc-egress-gateways.kubeovn.io -A -o wide 2>&1
  diag "controller log"
  kubectl -n "$CONTROLLER_NS" logs deploy/proxmox-network-controller --tail=200 2>&1
  return 0
}

phase=${1:-}
case $phase in
  preflight|vpc|control|datapath|capi|resilience|lifecycle|teardown|diagnostics) ;;
  *) echo "usage: $0 preflight|vpc|control|datapath|capi|resilience|lifecycle|teardown|diagnostics" >&2; exit 2 ;;
esac
if [ -z "$NS_A" ] || [ -z "$NS_B" ]; then
  echo "COZY_PXNET_NS_A and COZY_PXNET_NS_B must name the two tenant namespaces" >&2
  exit 2
fi

# The zone and its provider network are read here, so every phase knows them
# whichever ran before it.
if [ -z "$ZONE" ]; then
  ZONE=$(kubectl get proxmoxnetworkzones -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
fi
if [ -z "$PROVIDER" ] && [ -n "$ZONE" ]; then
  PROVIDER=$(kubectl get proxmoxnetworkzone "$ZONE" -o jsonpath='{.spec.providerNetwork}' 2>/dev/null)
fi

case $phase in
  preflight)   preflight ;;
  vpc)         vpc_phase ;;
  control)     control ;;
  datapath)    datapath ;;
  capi)        capi ;;
  resilience)  resilience ;;
  lifecycle)   lifecycle ;;
  teardown)    teardown ;;
  diagnostics) diagnostics; exit 0 ;;
esac

# SKIP and TODO lines are reported, never counted as failures.
diag "$phase: $n checks: $((n - failed - skipped - todos)) passed, $failed failed, $skipped skipped, $todos todo"
[ "$failed" -eq 0 ]
