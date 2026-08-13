# shellcheck shell=sh
# Shared probe + attribution helpers for the site-router e2e suite. Sourced by
# the reachability, MSS and negative-security script steps (a Chainsaw script's
# cwd is the suite directory, so `. ./_probe-lib.sh` resolves here).
#
# T13 live implementation. These drive the real topology on the cluster:
#   - remote-site-b : a VyOS gateway VM (the IPsec peer). It is driven over SSH
#     (user `vyos`, password ${REMOTE_B_PASS:-e2edrive} set by the guest's
#     first-boot config) so probes are sourced from its 192.0.2.x / test dummy
#     interfaces INTO the tunnel toward A.
#   - site-router-a : the gateway under test. Its guest firewall counters are
#     read over the VyOS HTTPS management API (Secret site-router-a-api-key) to
#     attribute guest-layer drops (TUNNEL-INGRESS default-action, input rule 1
#     Boundary-A, forward default-action).
#   - Cilium-layer drops are attributed from the datapath drop monitor
#     (`cilium monitor --type drop`) on A's node. Hubble need NOT be enabled; if
#     it is, `hubble observe` would also work, but cilium-monitor is the
#     always-available mechanism and is what hubble_dropped consumes.
#
# A helper pod `probe-driver` (with sshpass + curl, in $NAMESPACE) is the exec
# host for SSH-to-B and the VyOS API curls; the negative-security step ensures it
# exists. Resource names/namespace come from $NAMESPACE (Chainsaw-provided) and
# the suite's fixed names (site-router-a, remote-site-b, site-router-backend).
#
# assert_dropped snapshots the guards (guest counters + a bounded cilium-monitor
# capture) BEFORE the probe, so a guard-check confirms the delta caused by THIS
# probe. Every helper fails loudly (non-zero) if its evidence is absent, so the
# gate can never pass vacuously.

set -u
PROBE_STATE_DIR="${PROBE_STATE_DIR:-/tmp/site-router-probe}"
mkdir -p "$PROBE_STATE_DIR" 2>/dev/null || true
REMOTE_B_PASS="${REMOTE_B_PASS:-e2edrive}"
PROBE_SRC="${PROBE_SRC:-192.0.2.1}"

# ── resolvers (cached across calls in the same shell) ────────────────────────
_b_ip() {
  [ -n "${_B_IP:-}" ] && { echo "$_B_IP"; return; }
  _B_IP=$(kubectl -n "$NAMESPACE" get vmi remote-site-b \
    -o jsonpath='{.status.interfaces[0].ipAddress}' 2>/dev/null)
  echo "$_B_IP"
}
_a_pod_ip() {
  [ -n "${_A_IP:-}" ] && { echo "$_A_IP"; return; }
  _A_IP=$(kubectl -n "$NAMESPACE" get pod -l vm.kubevirt.io/name=site-router-a \
    -o jsonpath='{.items[0].status.podIP}' 2>/dev/null)
  echo "$_A_IP"
}
_a_token() {
  [ -n "${_A_TOKEN:-}" ] && { echo "$_A_TOKEN"; return; }
  _A_TOKEN=$(kubectl -n "$NAMESPACE" get secret site-router-a-api-key \
    -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null)
  echo "$_A_TOKEN"
}
_a_cilium_pod() {
  [ -n "${_A_CIL:-}" ] && { echo "$_A_CIL"; return; }
  node=$(kubectl -n "$NAMESPACE" get pod -l vm.kubevirt.io/name=site-router-a \
    -o jsonpath='{.items[0].spec.nodeName}' 2>/dev/null)
  _A_CIL=$(kubectl -n cozy-cilium get pod -l k8s-app=cilium \
    --field-selector "spec.nodeName=$node" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
  echo "$_A_CIL"
}
# ssh into B's guest and run the single command string "$1"
_ssh_b() {
  kubectl -n "$NAMESPACE" exec -i probe-driver -- sh -c \
    "sshpass -p '$REMOTE_B_PASS' ssh -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 vyos@$(_b_ip) \"$1\"" 2>/dev/null
}
# read a VyOS firewall counter (packets) from A over the management API.
#   $1 = selector: "name TUNNEL-INGRESS" | "input filter" | "forward filter"
#   $2 = rule id, or "default" for the default-action counter
_a_counter() {
  path=$(printf '%s' "$1" | sed 's/ /","/g')
  kubectl -n "$NAMESPACE" exec -i probe-driver -- sh -c \
    "curl -k -s --max-time 15 https://$(_a_pod_ip)/show \
      --form-string 'data={\"op\":\"show\",\"path\":[\"firewall\",\"ipv4\",\"$path\"]}' \
      --form-string 'key=$(_a_token)'" 2>/dev/null | python3 -c '
import sys,json
rule=sys.argv[1]
d=json.load(sys.stdin).get("data","")
for l in d.splitlines():
    f=l.split()
    if not f: continue
    if (rule=="default" and f[0]=="default") or (rule!="default" and f[0]==rule):
        print(f[3] if len(f)>3 else "0"); break
else:
    print("0")
' "$2" 2>/dev/null
}

# probe_from_b <proto> <dst> <port>
#   One probe from B's guest, sourced $PROBE_SRC, into the tunnel toward A.
#   Return 0 if REACHABLE, non-zero if not.
probe_from_b() {
  proto=$1; dst=$2; port=${3:-}
  case "$proto" in
    tcp)  _ssh_b "nc -z -w3 -s $PROBE_SRC $dst $port >/dev/null 2>&1 && echo R" | grep -q R ;;
    udp)  _ssh_b "echo x | nc -u -w2 -s $PROBE_SRC $dst $port >/dev/null 2>&1 && echo R" | grep -q R ;;
    icmp) _ssh_b "ping -c2 -W2 -I $PROBE_SRC $dst >/dev/null 2>&1 && echo R" | grep -q R ;;
    *) echo "probe_from_b: unknown proto $proto" >&2; return 2 ;;
  esac
}

# probe_from_b_stdout <proto> <dst> <port> <path>
#   HTTP GET from B sourced $PROBE_SRC; echo the RESPONSE BODY on stdout (used for
#   the source-preservation check: curl B → backend /clientip).
probe_from_b_stdout() {
  dst=$2; port=$3; path=${4:-/}
  _ssh_b "curl -s --max-time 8 --interface $PROBE_SRC http://$dst:$port$path"
}

# transfer_from_b <dst> <port> <megabytes>
#   Push <megabytes> MB from B across the tunnel via an HTTP POST to agnhost
#   netexec's /upload, and require the whole body to be accepted. This is the
#   bulk B -> backend transfer the MSS clamp protects: without the clamp the
#   post-decrypt segments black-hole and the POST hangs until the caller's step
#   timeout. (agnhost /download is NOT a sized-payload endpoint — it returns a
#   short timestamp stub regardless of ?size= — so an upload is the reliable
#   multi-MB path.) Return 0 on a completed transfer.
transfer_from_b() {
  dst=$1; port=$2; mb=${3:-8}; bytes=$((mb*1024*1024))
  sent=$(_ssh_b "head -c $bytes /dev/zero | curl -s --max-time 60 --interface $PROBE_SRC -o /dev/null -w '%{size_upload}' -X POST --data-binary @- 'http://$dst:$port/upload'")
  sent=${sent%%.*}
  [ "${sent:-0}" -ge "$bytes" ] 2>/dev/null
}

# hubble_dropped <dst> <reason-substring>
#   Assert a Cilium DROPPED flow toward <dst> matching <reason> from the
#   cilium-monitor capture taken over the probe window (POLICY_DENIED maps to
#   cilium-monitor's "Policy denied"). Hubble need not be enabled.
hubble_dropped() {
  dst=$1; reason=${2:-Policy denied}
  case "$reason" in POLICY_DENIED) reason="Policy denied" ;; esac
  cap="$PROBE_STATE_DIR/cilmon.log"
  [ -f "$cap" ] || { echo "hubble_dropped: no cilium-monitor capture" >&2; return 1; }
  grep -F "$dst" "$cap" | grep -qi "$reason"
}

# guest_counter_incremented <ruleset> <rule>
#   Assert a VyOS drop counter on A advanced vs the assert_dropped baseline.
#   Ruleset tokens: "TUNNEL-INGRESS default" | "input 1" | "forward default".
guest_counter_incremented() {
  case "$1" in
    TUNNEL-INGRESS) sel="name TUNNEL-INGRESS" ;;
    input)          sel="input filter" ;;
    forward)        sel="forward filter" ;;
    *) echo "guest_counter_incremented: unknown ruleset $1" >&2; return 2 ;;
  esac
  # The negative-security step passes the default row as "default-action";
  # `show firewall` prints it as "default". Normalise either spelling.
  rule=$2; case "$rule" in default-action) rule=default ;; esac
  before=$(cat "$PROBE_STATE_DIR/ctr_${1}_${rule}" 2>/dev/null || echo 0)
  after=$(_a_counter "$sel" "$rule")
  [ "${after:-0}" -gt "${before:-0}" ] 2>/dev/null
}

# guest_counter_flat <ruleset> <rule>
#   Assert a VyOS drop counter on A did NOT advance vs the assert_dropped
#   baseline — positive evidence the probe was dropped UPSTREAM of this guard.
#   Used for the undeclared-source case: the packet is rejected at A's IPsec SA
#   traffic-selector (its source is outside the negotiated remote traffic
#   selector, i.e. outside remoteCIDRs) and is never decrypted, so it never
#   reaches the TUNNEL-INGRESS chain and that chain's default-action counter
#   stays flat. Fails loudly if the counter cannot be read, so "flat" is always
#   backed by a real reading rather than a missing one.
guest_counter_flat() {
  case "$1" in
    TUNNEL-INGRESS) sel="name TUNNEL-INGRESS" ;;
    input)          sel="input filter" ;;
    forward)        sel="forward filter" ;;
    *) echo "guest_counter_flat: unknown ruleset $1" >&2; return 2 ;;
  esac
  rule=$2; case "$rule" in default-action) rule=default ;; esac
  before=$(cat "$PROBE_STATE_DIR/ctr_${1}_${rule}" 2>/dev/null || echo 0)
  after=$(_a_counter "$sel" "$rule")
  case "${after:-}" in ''|*[!0-9]*) echo "guest_counter_flat: could not read counter $1/$rule" >&2; return 1 ;; esac
  [ "${after:-0}" -le "${before:-0}" ] 2>/dev/null
}

# _snapshot_guards — assert_dropped's pre-probe hook: snapshot each guest counter
# of interest and start a bounded cilium-monitor drop capture on A's node.
_snapshot_guards() {
  for pair in "TUNNEL-INGRESS default" "input 1" "forward default"; do
    # shellcheck disable=SC2086
    set -- $pair
    case "$1" in
      TUNNEL-INGRESS) sel="name TUNNEL-INGRESS" ;;
      input) sel="input filter" ;;
      forward) sel="forward filter" ;;
    esac
    _a_counter "$sel" "$2" > "$PROBE_STATE_DIR/ctr_${1}_${2}" 2>/dev/null
  done
  cil=$(_a_cilium_pod)
  kubectl -n cozy-cilium exec "$cil" -- sh -c 'timeout 14 cilium monitor --type drop' \
    > "$PROBE_STATE_DIR/cilmon.log" 2>/dev/null &
  sleep 3   # let the monitor attach before the probe fires
}

# assert_dropped <desc> <guard-check> <proto> <dst> [port]
#   The negative-security primitive: a probe from B to <dst> MUST NOT reach it,
#   AND the named guard MUST be the one that dropped it (attribution — the design
#   requires establishing the responsible guard, not merely absence of
#   connectivity). Snapshots the guards first so the guard-check proves the delta.
assert_dropped() {
  desc=$1; guard=$2; proto=$3; dst=$4; port=${5:-}
  _snapshot_guards
  if probe_from_b "$proto" "$dst" "$port"; then
    echo "FAIL [$desc]: B reached $dst:$port — expected DROP" >&2
    return 1
  fi
  sleep 3   # let counters settle / the monitor flush
  # Run the guard with `eval`, NOT `sh -c`: the guard is a shell FUNCTION defined
  # in this sourced lib, so a child `sh -c` cannot see it. An unimplemented/failed
  # guard returns non-zero → the gate still fails.
  if ! eval "$guard"; then
    echo "FAIL [$desc]: $dst unreachable but the drop was NOT attributed to the expected guard ($guard)" >&2
    return 1
  fi
  echo "OK [$desc]: $dst dropped and attributed to its guard"
}
