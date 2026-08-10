#!/bin/sh
###############################################################################
# cozytest.sh - Bats-compatible test runner with live trace and enhanced      #
# output, written in pure shell                                               #
###############################################################################
set -eu

TEST_FILE=${1:?Usage: ./cozytest.sh <file.bats> [pattern]}
PATTERN=${2:-*}
LINE='----------------------------------------------------------------'

cols() { stty size 2>/dev/null | awk '{print $2}' || echo 80; }
if [ -t 1 ]; then
  MAXW=$(( $(cols) - 12 )); [ "$MAXW" -lt 40 ] && MAXW=70
else
  MAXW=0  # no truncation when not a tty (e.g. CI)
fi
BEGIN=$(date +%s)
timestamp() { s=$(( $(date +%s) - BEGIN )); printf '[%02d:%02d]' $((s/60)) $((s%60)); }

###############################################################################
# run_one <fn> <title>                                                        #
###############################################################################
run_one() {
  fn=$1 title=$2
  tmp=$(mktemp -d) || { echo "Failed to create temp directory" >&2; exit 1; }
  log="$tmp/log"

  echo "╭ » Run test: $title"
  START=$(date +%s)
  skip_next="+ $fn"

  {
    (
      PS4='+ '           # prefix for set -x
      set -eu -x         # strict + trace
      "$fn"
    )
    printf '__RC__%s\n' "$?"
  } 2>&1 | tee "$log" | while IFS= read -r line; do
        case "$line" in
          '__RC__'*) : ;;
          '+ '*)   cmd=${line#'+ '}
                    [ "$cmd" = "${skip_next#+ }" ] && continue
                    case "$cmd" in
                      'set -e'|'set -x'|'set -u'|'return 0') continue ;;
                    esac
                    out=$cmd ;;
          *)       out=$line ;;
        esac
        now=$(( $(date +%s) - START ))
        [ "$MAXW" -gt 0 ] && [ ${#out} -gt "$MAXW" ] && out="$(printf '%.*s…' "$MAXW" "$out")"
        printf '┊[%02d:%02d] %s\n' $((now/60)) $((now%60)) "$out"
  done

  rc=$(awk '/^__RC__/ {print substr($0,7)}' "$log" | tail -n1)
  [ -z "$rc" ] && rc=1
  now=$(( $(date +%s) - START ))

  if [ "$rc" -eq 0 ]; then
    printf '╰[%02d:%02d] ✅ Test OK: %s\n' $((now/60)) $((now%60)) "$title"
  else
    printf '╰[%02d:%02d] ❌ Test failed: %s (exit %s)\n' \
           $((now/60)) $((now%60)) "$title" "$rc"
    echo "----- captured output -----------------------------------------"
    grep -v '^__RC__' "$log"
    echo "$LINE"
    rm -rf "$tmp"
    exit "$rc"
  fi

  rm -rf "$tmp"
}

###############################################################################
# convert .bats -> shell-functions                                            #
###############################################################################
TMP_SH=$(mktemp) || { echo "Failed to create temp file" >&2; exit 1; }

# Per-file lifecycle hook. cozytest.sh runs each .bats as a single invocation
# and exit()s on the first failing @test, so this EXIT trap is the one place to:
#   1. on failure, snapshot the HOST cluster with crust-gather BEFORE any cleanup,
#      so each failed test keeps its own inspectable state instead of one
#      end-of-suite dump;
#   2. ALWAYS run the file's cozy_cleanup() if it defines one, so a test never
#      leaks resources into the shared tenant-test namespace (left-behind PVCs
#      otherwise exhaust the tenant quota and cascade-fail every later app).
# cozy_cleanup is a plain shell function a .bats file may define — there are no
# bats setup/teardown directives here, this runner only knows @test + bash.
# NOTE: nested tenant clusters are NOT captured here. This trap runs in the
# parent shell after the failing test subshell has exited and reaped its
# port-forward, and crust-gather can only reach a tenant via that localhost
# forward — so a test that creates tenant clusters (run-kubernetes.sh) captures
# them from its OWN in-subshell EXIT trap, while the forward is still alive.
COZY_REPORT_DIR="${COZY_REPORT_DIR:-_out/cozyreport}"
# Which suites get the cluster captures below. They read cluster state, so they
# belong to the e2e suites and to nothing else. `command -v kubectl` does not
# tell the two apart: a unit runner has the binary and no cluster, so a red unit
# test used to walk into the legs below on its way out.
#
# What that costs depends on how kubectl fails. Against an endpoint that refuses
# immediately it is a second; against one that hangs -- an unreachable address, a
# context left pointing at a torn-down cluster -- it was measured at ~28s for the
# previous-logs leg, which self-bounds its pod list and so never reaches the 300s
# ceiling set below it. The data-plane leg bounds its own reads as well, 28s for
# a list and 20s for a single read, so it returns in tens of seconds rather than
# holding its 600s ceiling. Dead waiting inside a job budget either way, for a
# failure that should cost seconds.
#
# Where crust-gather is installed as well the bill stops being empty and starts
# being wrong: that leg does not need a reachable cluster to be pointless, it
# needs an ambient KUBECONFIG, and it can hold the trap for 390s -- its 360s
# deadline plus the -k 30 kill grace -- snapshotting whatever cluster the
# current context happens to name.
#
# The discriminator is the e2e- prefix, on the suite's own name or on the
# directory holding it. At the top level the prefix is what the project already
# sorts on: the Makefile builds BATS_UNIT_FILES as
# $(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats)), so a top-level e2e-*
# suite is precisely one `make unit-tests` refuses to run and
# packages/core/testing/Makefile runs against a live cluster instead.
#
# That wildcard is not recursive, so it says nothing about subdirectories, and
# hack/e2e-apps/ holds live-cluster suites whose own filenames carry no prefix.
# Matching the directory too keeps them armed; matching only the basename would
# take the captures away from suites that need them, which is the opposite of
# the fix. Neither test is a claim that every future live-cluster suite will be
# named this way -- it is the only signal the runner has, and a suite placed
# outside both shapes gets no captures.
#
# Deliberately not a reachability probe. Gating on "can I talk to an apiserver"
# would disarm the captures in the case they exist for: a failing e2e run is
# often one whose apiserver is degraded, and a probe answering "no" there would
# skip the snapshot precisely when it is the evidence.
case "$(basename "$TEST_FILE")" in
  e2e-*) _cozy_cluster_captures=1 ;;
  *)
    case "$(basename "$(dirname "$TEST_FILE")")" in
      e2e-*) _cozy_cluster_captures=1 ;;
      *)     _cozy_cluster_captures=0 ;;
    esac
    ;;
esac
_cozy_on_exit() {
  _rc=$?
  if [ "$_rc" -ne 0 ]; then
    _snap="$COZY_REPORT_DIR/snapshots/$(basename "$TEST_FILE" .bats)"
    mkdir -p "$_snap" 2>/dev/null || true
    # Previous-instance container logs for anything that crash-looped. `kubectl
    # logs` shows only the current instance, so for a crash-looping pod the
    # decisive evidence — the immediately preceding run — is reachable only via
    # `--previous` and is lost once the kubelet garbage-collects that container.
    # FIRST, ahead of both captures below: it is the cheapest leg and the only
    # one whose subject is perishable. The kubelet retains one dead container
    # per pod by default, so each further restart of a still-looping pod drops
    # the instance we are here to read — and the snapshot leg alone can hold the
    # trap for 390s. The other two captures read state that keeps.
    if [ "$_cozy_cluster_captures" -eq 1 ] && command -v kubectl >/dev/null 2>&1 && [ -x "$(dirname "$0")/e2e-capture-previous-logs.sh" ]; then
      _prev_rc=0
      timeout -k 30 300 "$(dirname "$0")/e2e-capture-previous-logs.sh" \
        "$_snap/previous-logs" "${COZY_TEST_NAMESPACE:-tenant-test}" 2>&1 || _prev_rc=$?
      # A backstop that fires must say so, matching the crust-gather leg below.
      # 300s sits above the ~270s worst case inside (a ~30s pod list plus up to
      # 12 × 20s log reads), so it should not fire; if it does, a silently cut
      # capture would read as a complete one listing fewer crash-loops than
      # actually occurred. This handler's whole worst case is ~1350s (330 + 390
      # + 630, each leg including its 30s kill grace) against a job capped at
      # 180 minutes with no per-step timeout —
      # deliberate, for the same reason recorded in hack/e2e-chainsaw/.chainsaw.yaml:
      # a bounded, honest wait beats a kill that collects nothing.
      if [ "$_prev_rc" -ne 0 ]; then
        echo "» previous-instance capture INCOMPLETE (exit $_prev_rc); kept what landed in $_snap/previous-logs"
      fi
    fi
    if [ "$_cozy_cluster_captures" -eq 1 ] && command -v crust-gather >/dev/null 2>&1; then
      echo "» capturing crust-gather snapshot of failed $(basename "$TEST_FILE") -> $_snap"
      # Bound with a timeout: crust-gather collect has hung indefinitely on a
      # contended/degraded cluster (e.g. streaming logs from a crashlooping pod),
      # wedging the whole test step for hours until the job-level cancel. 5 min is
      # ample for a host snapshot; a partial capture (timeout exits 124, swallowed
      # by `|| true`) still beats a multi-hour hang. -k 30 hard-kills if a blocked
      # collect ignores the SIGTERM.
      # --duration is crust-gather's own budget for the collection phase and
      # defaults to 60s, which silently truncates a snapshot of a cluster this
      # size: on elapse it abandons the collection and skips its finish step,
      # leaving a partial tree with no indication that the rest is missing. Set
      # it explicitly. The outer wall-clock must exceed it because crust-gather
      # runs API discovery BEFORE that timer starts and that phase is unbounded
      # — with an unhealthy aggregated APIService (the state a failed run is
      # usually in) discovery alone can eat the whole budget before a single
      # object is written.
      # Output goes to a log inside the snapshot instead of /dev/null so
      # "is this snapshot complete or truncated?" is answerable from the
      # uploaded artifact rather than guessed, matching the Chainsaw catch.
      _cg_rc=0
      # --disable-additional-logs: the host-log leg spawns a privileged debug pod per
      # node, which baseline PodSecurity rejects (403); its retry loop then burns the
      # whole --duration and crust-gather exits 1. Skip it — it collects nothing here.
      timeout -k 30 360 crust-gather collect --disable-additional-logs --duration 180s \
        --exclude-kind Secret -f "$_snap/host" >"$_snap/crust-gather.log" 2>&1 || _cg_rc=$?
      case "$_cg_rc" in
        0) echo "» crust-gather host snapshot complete" ;;
        124 | 137) echo "» crust-gather host snapshot TRUNCATED (wall-clock $_cg_rc); partial state kept, see $_snap/crust-gather.log" ;;
        *) echo "» crust-gather host snapshot FAILED (exit $_cg_rc); see $_snap/crust-gather.log" ;;
      esac
    fi
    # Diagnostic-only: capture the host->pod CNI data-plane state for any
    # NotReady pod so the recurrent host->local-pod "connection refused"
    # transient (rooted in our cilium+kube-ovn chaining:
    # enable-host-legacy-routing + CNI InstallEndpointRoute:false, which
    # delegates host->local-pod routing to kube-ovn/ovn0) can be root-caused
    # from the uploaded artifact. crust-gather captures object state but not the
    # node's L3 forwarding state. This NEVER affects the test outcome: every
    # capture inside is time-boxed and `|| true`, the whole run is wrapped in a
    # wall-clock backstop so it cannot stall the job, and the backstop's own
    # status is read only to print a line. It no-ops when there are no affected
    # pods or when kubectl/the tooling is absent.
    # The `-x` check matches the previous-logs leg above and the Chainsaw
    # caller, and it stopped being cosmetic once the status below is printed: a
    # tree without the collector would otherwise report "INCOMPLETE (exit 127);
    # kept what landed" about a directory nothing ever created.
    if [ "$_cozy_cluster_captures" -eq 1 ] && command -v kubectl >/dev/null 2>&1 && [ -x "$(dirname "$0")/e2e-capture-dataplane.sh" ]; then
      echo "» capturing host->pod data-plane for NotReady pods -> $_snap/dataplane"
      _dp_rc=0
      timeout -k 30 600 "$(dirname "$0")/e2e-capture-dataplane.sh" "$_snap/dataplane" 2>&1 || _dp_rc=$?
      # Read, not propagated: the status still cannot change the job's outcome,
      # it only gets a line. The collector names every read of its own that it
      # could not finish, which leaves this backstop as the one remaining way
      # for that leg to die silently -- and a truncated dataplane/ is otherwise
      # indistinguishable from a complete one that found little. Same discipline
      # as the previous-logs leg above, whose comment carries the arithmetic.
      if [ "$_dp_rc" -ne 0 ]; then
        echo "» data-plane capture INCOMPLETE (exit $_dp_rc); kept what landed in $_snap/dataplane"
      fi
    fi
  fi
  if command -v cozy_cleanup >/dev/null 2>&1; then
    echo "» cozy_cleanup $(basename "$TEST_FILE" .bats)"
    cozy_cleanup || true
  fi
  rm -f "$TMP_SH"
}
trap '_cozy_on_exit' EXIT
awk '
  /^@test[[:space:]]+"/ {
    line  = substr($0, index($0, "\"") + 1)
    title = substr(line, 1, index(line, "\"") - 1)
    fname = "test_"
    for (i = 1; i <= length(title); i++) {
      c = substr(title, i, 1)
      fname = fname (c ~ /[A-Za-z0-9]/ ? c : "_")
    }
    printf("### %s\n", title)
    printf("%s() {\n", fname)
    print "  set -e"
    next
  }
  /^}$/ {
    print "  return 0"
    print "}"
    next
  }
  { print }
' "$TEST_FILE" > "$TMP_SH"

[ -f "$TMP_SH" ] || { echo "Failed to generate test functions" >&2; exit 1; }
# shellcheck disable=SC1090
. "$TMP_SH"

###############################################################################
# run selected tests                                                          #
###############################################################################
awk -v pat="$PATTERN" '
  /^### / {
    title = substr($0, 5)
    name = "test_"
    for (i = 1; i <= length(title); i++) {
      c = substr(title, i, 1)
      name = name (c ~ /[A-Za-z0-9]/ ? c : "_")
    }
    if (pat == "*" || index(title, pat) > 0)
      printf("%s %s\n", name, title)
  }
' "$TMP_SH" | while IFS=' ' read -r fn title; do
  run_one "$fn" "$title"
done
