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

# Run one crust-gather collect, capturing its full output + exit code into a
# sibling .collect.log. A failed collect then leaves a breadcrumb inside the
# uploaded artifact instead of vanishing into /dev/null (the previous behaviour
# silently dropped snapshots with no clue why). Bounded by `timeout -k 30 5m`
# so a wedged/degraded cluster can't stall the whole job (-k hard-kills a
# collect that ignores the SIGTERM). The caller supplies the filter flags; the
# helper only owns -f, the timeout and logging.
#
# crust-gather exits non-zero when its --duration window closes with a scanner
# still in flight, even though the API tree and pod logs are already on disk —
# so a non-zero rc that still produced resources is reported as PARTIAL, not
# FAILED. A 100+MB usable snapshot must not be mistaken for an empty one and
# discarded over a scary label. $1=label $2=dest-dir [crust-gather collect args…].
_cozy_collect() {
  _label=$1; _dest=$2; shift 2
  if timeout -k 30 5m crust-gather collect -f "$_dest" "$@" >"$_dest.collect.log" 2>&1; then
    echo "  ✓ crust-gather $_label -> $_dest"
  else
    # First statement in the else, so $? is still the failed collect's status
    # (after `fi` it would be the if-compound's 0). Treat a non-zero rc that
    # nonetheless wrote a resource tree as PARTIAL rather than FAILED.
    _crc=$?
    if [ -d "$_dest/cluster" ] || find "$_dest/namespaces" -name '*.yaml' -print -quit 2>/dev/null | grep -q .; then
      echo "  ⚠ crust-gather $_label PARTIAL (rc=$_crc) — $(tail -n1 "$_dest.collect.log"); see $_dest.collect.log"
    else
      echo "  ✗ crust-gather $_label FAILED (rc=$_crc) — see $_dest.collect.log"
    fi
  fi
}

# Helm stores each release as a kind=Secret object (sh.helm.release.v1.<rel>.v<n>),
# so the main dump below drops it along with every other Secret. This dedicated
# include-only collect captures JUST those secrets — verified AND-composition:
# crust-gather puts all CLI filters in one FilterList evaluated with .all(), so
# `--include-kind Secret --include-name <re>` keeps a Secret only when BOTH match.
# It carries each release's revision history + status, which is what explains a
# HelmRelease wedged mid-install. $1=label $2=dest-dir [extra args e.g. -k kc].
_cozy_collect_helm() {
  _label=$1; _dest=$2; shift 2
  _cozy_collect "$_label" "$_dest" \
    --include-kind Secret --include-name 'sh\.helm\.release\..*' "$@"
}

_cozy_on_exit() {
  _rc=$?
  if [ "$_rc" -ne 0 ]; then
    _snap="$COZY_REPORT_DIR/snapshots/$(basename "$TEST_FILE" .bats)"
    mkdir -p "$_snap" 2>/dev/null || true
    if command -v crust-gather >/dev/null 2>&1; then
      echo "» capturing crust-gather snapshot of failed $(basename "$TEST_FILE") -> $_snap"
      # --disable-additional-logs: skip the node host-log scanner. It spawns a
      # privileged busybox debug pod per node which PodSecurity rejects (403) in
      # cozystack's baseline/restricted namespaces, then retries until --duration
      # elapses — burning the deadline and forcing a non-zero exit while the
      # already-collected API tree + container logs sit on disk. Talos host logs
      # aren't reachable that way anyway, and aren't needed for app-level failures.
      _cozy_collect host "$_snap/host" --exclude-kind Secret --disable-additional-logs
      # Helm release storage is excluded by --exclude-kind Secret above; grab it
      # separately so a wedged HelmRelease's revision history is in the artifact.
      _cozy_collect_helm host-helm "$_snap/host-helm"
    fi
    # Diagnostic-only: capture the host->pod CNI data-plane state for any
    # NotReady pod so the recurrent host->local-pod "connection refused"
    # transient (rooted in our cilium+kube-ovn chaining:
    # enable-host-legacy-routing + CNI InstallEndpointRoute:false, which
    # delegates host->local-pod routing to kube-ovn/ovn0) can be root-caused
    # from the uploaded artifact. crust-gather captures object state but not the
    # node's L3 forwarding state. This NEVER affects the test outcome: every
    # capture is time-boxed and `|| true`, and the whole run is wrapped in a
    # wall-clock backstop so it cannot stall the job. It no-ops when there are no
    # affected pods or when kubectl/the tooling is absent.
    if command -v kubectl >/dev/null 2>&1; then
      echo "» capturing host->pod data-plane for NotReady pods -> $_snap/dataplane"
      timeout -k 30 600 "$(dirname "$0")/e2e-capture-dataplane.sh" "$_snap/dataplane" 2>&1 || true
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
