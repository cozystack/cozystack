#!/usr/bin/env bats
# Behavioural tests for the monitoring-agents pre-delete hook:
#
#   packages/system/monitoring-agents/templates/vm-cleanup-hook.yaml
#
# The script is extracted from the rendered chart and RUN against a fake
# kubectl that keeps the VictoriaMetrics objects in a state file, with fake
# `date` and `sleep` so the 60s grace period passes in an instant. The
# helm-unittest suite in packages/system/monitoring-agents/tests/
# uninstall_test.yaml matches the script text instead, and stayed green when the
# ownership condition was removed, when the finalizer patch was made a no-op and
# when the polling condition was inverted. Each of those turns this file red.
#
# What is pinned here:
#   * only objects whose Helm annotations name this release, by name and
#     namespace both, are deleted or patched: an object of another release, of
#     a same-named release in another namespace, or one Helm does not own, is
#     left alone;
#   * with the operator gone, the hook waits before touching finalizers, then
#     clears them, and nothing of this release is left behind;
#   * with the operator alive, it finalizes the objects itself and the hook
#     never patches a finalizer;
#   * a kind whose CRD is gone, or a release with nothing left, exits 0, since a
#     failing pre-delete hook would itself block the uninstall.
#
#   * past the grace period only apps.victoriametrics.com/finalizer is removed,
#     and only from an object already being deleted: another controller's
#     finalizer on the same object, and an object whose delete did not go
#     through, are left as they are.
#
# Run from the repo root with make bats-unit-tests. Direct status checks
# retain compatibility with cozytest, which has no Bats run/setup helpers.
# Inline cleanup preserves Bats' TAP handler on failed assertions.

load test_helper

MA_CHART=packages/system/monitoring-agents
MA_RELEASE=monitoring-agents
MA_NAMESPACE=cozy-monitoring

# render_hook <job-name> <out>
# Extract the named Job's shell script from the rendered chart into <out>. The
# non-empty check must be an early `return 1`: hack/cozytest.sh rewrites every
# line matching ^}$ into `return 0` plus `}`, helpers included, so a check left
# in last-command position has its status discarded.
render_hook() {
  helm template "$MA_RELEASE" "$MA_CHART" --namespace "$MA_NAMESPACE" \
    | yq eval "select(.kind == \"Job\" and .metadata.name == \"$1\") | .spec.template.spec.containers[0].command[2]" - > "$2"
  [ -s "$2" ] || return 1
}

# write_fakes <dir>
# Emit fake `kubectl`, `date` and `sleep` into <dir>.
#
# kubectl reads and writes $STATE, one object per line as
# kind|release-name|release-namespace|namespace|name|finalizers|deleting[|uid], where
# the two release fields are the Helm annotations (empty when Helm does not own
# the object), finalizers is a comma-separated list and deleting is 1 once the
# object has a deletionTimestamp. A listing `get` prints this kind's objects in
# the hook's jsonpath format, or fails for a kind listed in $MISSING_KINDS as
# kubectl does when the CRD is gone; a `get` of one object prints its
# deletionTimestamp and finalizers in the hook's format. `delete` fails for a
# name listed in $UNDELETABLE; otherwise it removes the object when
# $OPERATOR_LIVE is 1, standing for an operator that finalizes it, or when it
# holds no finalizer, and leaves it Terminating otherwise. `patch` applies a
# merge patch that nulls the finalizers, or a JSON patch whose test op matches
# the entry its remove op names, and removes an object that is left Terminating
# with no finalizer. Every call goes to $KLOG.
#
# date advances 1s per call from $CLOCK, so the 60s wait ends after sixty
# of polls, and records each time it returns in $KLOG; sleep records itself,
# and with $SLEEP_ADVANCES=1 also moves $CLOCK by its argument. The log thus
# shows how much fake time passed before each patch.
# No line below is a bare column-0 `}`, so cozytest.sh's parser leaves it alone.
write_fakes() {
  cat > "$1/kubectl" <<'KEOF'
#!/bin/sh
echo "kubectl $*" >> "$KLOG"
# put <kind> <ns> <name> <finalizers> <deleting>: rewrite the row, or drop it
# when it is Terminating with nothing left to hold it.
put() {
  awk -F'|' -v OFS='|' -v k="$1" -v ns="$2" -v n="$3" -v f="$4" -v d="$5" -v uid="${6:-}" '
    $1 == k && $4 == ns && $5 == n { if (d == 1 && f == "") next; $6 = f; $7 = d; if (uid != "") $8 = uid }
    { print }' "$STATE" > "$STATE.new"
  mv "$STATE.new" "$STATE"; }
case "$1" in
  get)
    kind=${2%%.*}
    case " ${MISSING_KINDS:-} " in
      *" $kind "*)
        echo "error: the server doesn't have a resource type \"$kind\"" >&2
        exit 1 ;;
    esac
    case "$*" in *'{.metadata.uid}'*) with_uid=1 ;; *) with_uid=0 ;; esac
    awk -F'|' -v k="$kind" -v with_uid="$with_uid" '$1 == k {
      row = $2 "|" $3 "|" $4 "|" $5
      if (with_uid) row = row "|" ($8 == "" ? $1 "-" $5 : $8)
      print row
    }' "$STATE"
    exit 0 ;;
  -n|--namespace)
    ns=$2 verb=$3 kind=${4%%.*} name=$5
    row=$(awk -F'|' -v k="$kind" -v ns="$ns" -v n="$name" '$1 == k && $4 == ns && $5 == n' "$STATE")
    if [ -z "$row" ]; then
      case "$*" in *--ignore-not-found*) exit 0 ;; esac
      echo "Error from server (NotFound): $kind \"$name\" not found" >&2
      exit 1
    fi
    fins=$(echo "$row" | cut -d'|' -f6)
    deleting=$(echo "$row" | cut -d'|' -f7)
    uid=$(echo "$row" | cut -d'|' -f8)
    uid=${uid:-"$kind-$name"}
    case "$verb" in
      get)
        case "$*" in *'{.metadata.uid}'*) printf '%s|' "$uid" ;; esac
        [ "$deleting" = 1 ] && printf '2026-01-01T00:00:00Z'
        printf '|'
        for f in $(echo "$fins" | tr ',' ' '); do printf '%s ' "$f"; done
        exit 0 ;;
      delete)
        case " ${UNDELETABLE:-} " in
          *" $name "*)
            echo "Error from server (Forbidden): $kind \"$name\" is forbidden" >&2
            exit 1 ;;
        esac
        [ "${OPERATOR_LIVE:-0}" = 1 ] && fins=""
        put "$kind" "$ns" "$name" "$fins" 1
        echo "$kind \"$name\" deleted"
        exit 0 ;;
      patch)
        if [ "$name" = "${RECREATE_ON_PATCH:-}" ] && [ "$uid" = "$kind-$name" ]; then
          uid="replacement-$uid"
          put "$kind" "$ns" "$name" "$fins" 1 "$uid"
        fi
        if [ "$name" = "${SHIFT_ON_PATCH:-}" ] && [ "${fins%%,*}" = example.com/finished ]; then
          fins=${fins#*,}
          put "$kind" "$ns" "$name" "$fins" "$deleting"
        fi
        [ "$name" != "${REFUSE_PATCH:-}" ] || exit 1
        expected_uid=$(printf '%s' "$*" | sed -n 's|.*"path":"/metadata/uid","value":"\([^"]*\)".*|\1|p')
        if [ -n "$expected_uid" ] && [ "$expected_uid" != "$uid" ]; then exit 1; fi
        case "$*" in
          *'"finalizers":null'*) fins="" ;;
          *'"op":"remove"'*)
            idx=$(echo "$*" | sed -n 's|.*"op":"remove","path":"/metadata/finalizers/\([0-9]*\)".*|\1|p')
            want=$(echo "$*" | sed -n 's|.*"op":"test","path":"/metadata/finalizers/[0-9]*","value":"\([^"]*\)".*|\1|p')
            have=$(echo "$fins" | tr ',' '\n' | sed -n "$((idx + 1))p")
            if [ -z "$idx" ] || [ "$have" != "$want" ]; then
              echo "The request is invalid: the server rejected our request due to an error in our request" >&2
              exit 1
            fi
            fins=$(echo "$fins" | tr ',' '\n' | sed "$((idx + 1))d" | paste -sd, -) ;;
          *)
            echo "fake kubectl: unexpected patch: $*" >&2
            exit 64 ;;
        esac
        put "$kind" "$ns" "$name" "$fins" "$deleting"
        echo "$kind \"$name\" patched"
        exit 0 ;;
    esac ;;
esac
echo "fake kubectl: unexpected call: $*" >&2
exit 64
KEOF
  cat > "$1/date" <<'KEOF'
#!/bin/sh
c=$(cat "$CLOCK" 2>/dev/null || echo 0)
echo $((c + 1)) > "$CLOCK"
echo "date $((1000000 + c))" >> "$KLOG"
echo $((1000000 + c))
KEOF
  cat > "$1/sleep" <<'KEOF'
#!/bin/sh
echo "sleep $*" >> "$KLOG"
if [ "${SLEEP_ADVANCES:-0}" = 1 ]; then
  c=$(cat "$CLOCK" 2>/dev/null || echo 0)
  echo $((c + $1)) > "$CLOCK"
fi
KEOF
  chmod +x "$1/kubectl" "$1/date" "$1/sleep"
}

# write_state <file>
# Three objects of this release in two namespaces; a VMRule of another release;
# a VMRule whose release name matches but whose release namespace does not, so
# only the pair of annotations tells it apart; and a VMServiceScrape that
# carries no Helm annotations.
write_state() {
  cat > "$1" <<EOF
vmagents|$MA_RELEASE|$MA_NAMESPACE|$MA_NAMESPACE|vmagent|apps.victoriametrics.com/finalizer|0
vmrules|$MA_RELEASE|$MA_NAMESPACE|$MA_NAMESPACE|alerts-node|apps.victoriametrics.com/finalizer|0
vmservicescrapes|$MA_RELEASE|$MA_NAMESPACE|cozy-victoria-metrics-operator|operator|apps.victoriametrics.com/finalizer|0
vmrules|other|$MA_NAMESPACE|$MA_NAMESPACE|foreign-rule|apps.victoriametrics.com/finalizer|0
vmrules|$MA_RELEASE|other-namespace|$MA_NAMESPACE|foreign-ns-rule|apps.victoriametrics.com/finalizer|0
vmservicescrapes|||$MA_NAMESPACE|unmanaged-scrape|apps.victoriametrics.com/finalizer|0
EOF
}

# run_hook <dir>
# Run the rendered hook with the fakes first on PATH; its status lands in $rc.
run_hook() {
  export KLOG="$1/calls" STATE="$1/state" CLOCK="$1/clock"
  : > "$KLOG"
  rc=0
  PATH="$1:$PATH" sh "$1/hook.sh" > "$1/out" 2> "$1/err" || rc=$?
}

@test "a render that produces no script fails instead of passing as a no-op" {
  # Guards the guard: every test below runs whatever render_hook produced, so an
  # empty render would satisfy the exit-0 assertions without executing a line of
  # the hook.
  tmp=$(mktemp -d)
  if render_hook no-such-job "$tmp/hook.sh"; then
    echo "FAIL: render_hook reported success for a Job name the chart never emits"
    false
  fi
  rm -rf "$tmp"
}

@test "vm cleanup hook clears the finalizers of this release's objects only, after the grace period" {
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  write_state "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export OPERATOR_LIVE=0 MISSING_KINDS=""
  run_hook "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  for obj in "vmagents.operator.victoriametrics.com vmagent" \
             "vmrules.operator.victoriametrics.com alerts-node" \
             "vmservicescrapes.operator.victoriametrics.com operator"; do
    if ! grep -q -- " delete $obj --wait=false" "$tmp/calls"; then
      echo "FAIL: $obj was not deleted"
      cat "$tmp/calls"
      false
    fi
  done
  # The objects of this release must be gone, which the fake only does for a
  # patch that really clears the finalizers.
  if grep -q "^[a-z]*|$MA_RELEASE|$MA_NAMESPACE|" "$tmp/state"; then
    echo "FAIL: objects of this release are still there, Terminating"
    cat "$tmp/state" "$tmp/calls"
    false
  fi
  # Another release's object, one of a same-named release in another
  # namespace, and an object Helm does not own are never touched.
  if grep -E -q -- ' (delete|patch) [a-z.]+ (foreign-rule|foreign-ns-rule|unmanaged-scrape)( |$)' "$tmp/calls"; then
    echo "FAIL: the hook touched an object this release does not own"
    cat "$tmp/calls"
    false
  fi
  if [ "$(grep -c '|' "$tmp/state")" -ne 3 ]; then
    echo "FAIL: the three objects this release does not own should remain"
    cat "$tmp/state"
    false
  fi
  # The operator gets its chance first: the hook polls and sleeps before the
  # first patch, and does not stop waiting before the 60s are up.
  first_patch=$(grep -n ' patch ' "$tmp/calls" | head -1 | cut -d: -f1)
  if [ -z "$first_patch" ]; then
    echo "FAIL: no finalizer was cleared"
    cat "$tmp/calls"
    false
  fi
  # The first date call sets the deadline; the last one before the first patch
  # is the time the hook gave up waiting.
  start=$(grep '^date ' "$tmp/calls" | head -1 | cut -d' ' -f2)
  waited_until=$(head -n "$first_patch" "$tmp/calls" | grep '^date ' | tail -1 | cut -d' ' -f2)
  if [ -z "$start" ] || [ $((waited_until - start)) -lt 60 ]; then
    echo "FAIL: finalizers cleared after $((waited_until - start))s; the 60s grace period was cut short"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "vm cleanup hook clears the finalizers before its Job's deadline" {
  # The grace-period test above keeps sleep free so a shortened wait shows; here
  # sleep costs its argument, so a poll interval long enough to overshoot the
  # Job's activeDeadlineSeconds fails instead of passing.
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  write_state "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  job_deadline=$(helm template "$MA_RELEASE" "$MA_CHART" --namespace "$MA_NAMESPACE" \
    | yq eval 'select(.kind == "Job" and .metadata.name == "vm-objects-cleanup") | .spec.activeDeadlineSeconds' -)
  export OPERATOR_LIVE=0 MISSING_KINDS="" SLEEP_ADVANCES=1
  run_hook "$tmp"
  unset SLEEP_ADVANCES

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  first_patch=$(grep -n ' patch ' "$tmp/calls" | head -1 | cut -d: -f1)
  start=$(grep '^date ' "$tmp/calls" | head -1 | cut -d' ' -f2)
  waited_until=$(head -n "${first_patch:-0}" "$tmp/calls" | grep '^date ' | tail -1 | cut -d' ' -f2)
  if [ -z "$first_patch" ] || [ -z "$job_deadline" ] || [ $((waited_until - start)) -ge "$job_deadline" ]; then
    echo "FAIL: first finalizer patch at $((waited_until - start))s, Job deadline ${job_deadline}s"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "vm cleanup hook leaves the finalizers to a live operator" {
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  write_state "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export OPERATOR_LIVE=1 MISSING_KINDS=""
  run_hook "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q ' delete vmagents.operator.victoriametrics.com vmagent ' "$tmp/calls"; then
    echo "FAIL: the VMAgent was not deleted"
    cat "$tmp/calls"
    false
  fi
  if grep -q ' patch ' "$tmp/calls"; then
    echo "FAIL: the hook cleared finalizers the live operator was handling"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "vm cleanup hook exits 0 and cleans the other kinds when a CRD is gone" {
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  write_state "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export OPERATOR_LIVE=0 MISSING_KINDS="vmrules vmpodscrapes vmnodescrapes"
  run_hook "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc with a CRD gone, which would block the uninstall"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if grep -q "^vmagents|$MA_RELEASE|" "$tmp/state"; then
    echo "FAIL: the VMAgent was left Terminating because another kind's CRD was gone"
    cat "$tmp/state" "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "vm cleanup hook exits 0 without deleting anything when the release owns nothing" {
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  printf 'vmrules|other|%s|%s|foreign-rule||0\nvmrules|%s|other-namespace|%s|foreign-ns-rule||0\n' \
    "$MA_NAMESPACE" "$MA_NAMESPACE" "$MA_RELEASE" "$MA_NAMESPACE" > "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export OPERATOR_LIVE=0 MISSING_KINDS=""
  run_hook "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc with nothing to clean"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if grep -E -q ' (delete|patch) ' "$tmp/calls"; then
    echo "FAIL: the hook deleted or patched with nothing of this release left"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "vm cleanup hook removes only the operator's finalizer and keeps another controller's" {
  # A merge patch nulling the finalizers drops every entry, so another
  # controller's cleanup is skipped. The operator's finalizer is put second so
  # the JSON patch has to name the right index, not just the first one.
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  printf 'vmrules|%s|%s|%s|mixed-rule|example.com/other,apps.victoriametrics.com/finalizer|0\n' \
    "$MA_RELEASE" "$MA_NAMESPACE" "$MA_NAMESPACE" > "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export OPERATOR_LIVE=0 MISSING_KINDS="" UNDELETABLE=""
  run_hook "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep --quiet '|mixed-rule|example.com/other|1$' "$tmp/state"; then
    echo "FAIL: the other controller's finalizer was not kept alone on the Terminating object"
    cat "$tmp/state" "$tmp/calls"
    false
  fi
  if ! grep --quiet ' patch vmrules.operator.victoriametrics.com mixed-rule ' "$tmp/calls"; then
    echo "FAIL: the operator's finalizer was never patched out"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "vm cleanup hook leaves the finalizers of an object that is not being deleted" {
  # A delete that did not go through leaves an object the cluster still wants;
  # clearing its finalizer would let a later delete skip the operator's cleanup.
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  write_state "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export OPERATOR_LIVE=0 MISSING_KINDS="" UNDELETABLE="alerts-node"
  run_hook "$tmp"
  unset UNDELETABLE

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if grep --quiet ' patch vmrules.operator.victoriametrics.com alerts-node ' "$tmp/calls"; then
    echo "FAIL: the hook patched the finalizer of an object that is not being deleted"
    cat "$tmp/calls"
    false
  fi
  if ! grep --quiet '|alerts-node|apps.victoriametrics.com/finalizer|0$' "$tmp/state"; then
    echo "FAIL: the object that is not being deleted lost its finalizer"
    cat "$tmp/state"
    false
  fi
  if grep --quiet '|vmagent|' "$tmp/state"; then
    echo "FAIL: one undeletable object kept the hook from finalizing the others"
    cat "$tmp/state" "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "vm cleanup hook preserves the finalizer on a same-name replacement" {
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  write_state "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export RECREATE_ON_PATCH=alerts-node
  run_hook "$tmp"
  kept=0
  if [ "$rc" -eq 0 ] && grep --quiet ' patch vmrules.operator.victoriametrics.com alerts-node ' "$tmp/calls" &&
     grep --quiet '|alerts-node|apps.victoriametrics.com/finalizer|1|replacement-vmrules-alerts-node$' "$tmp/state"; then
    kept=1
  fi
  rm -rf "$tmp"
  [ "$kept" = 1 ] || return 1
}

@test "vm cleanup retries when another controller removes its finalizer first" {
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  printf 'vmrules|%s|%s|%s|mixed|example.com/finished,apps.victoriametrics.com/finalizer,example.com/keep|0\n' \
    "$MA_RELEASE" "$MA_NAMESPACE" "$MA_NAMESPACE" > "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export SHIFT_ON_PATCH=mixed SLEEP_ADVANCES=1
  run_hook "$tmp"
  kept=0
  if [ "$rc" -eq 0 ] && [ "$(grep --count ' patch .* mixed ' "$tmp/calls")" = 2 ] &&
     grep --quiet '|mixed|example.com/keep|1$' "$tmp/state"; then kept=1; fi
  if [ "$kept" = 0 ]; then cat "$tmp/state" "$tmp/err"; fi
  rm -rf "$tmp"
  [ "$kept" = 1 ] || return 1
}

@test "vm cleanup reports a persistent patch failure and processes the other objects" {
  tmp=$(mktemp -d)
  write_fakes "$tmp"
  write_state "$tmp/state"
  render_hook vm-objects-cleanup "$tmp/hook.sh"
  export REFUSE_PATCH=alerts-node SLEEP_ADVANCES=1
  run_hook "$tmp"
  bounded=0
  if [ "$rc" -ne 0 ] && [ "$(cat "$tmp/clock")" -lt 180 ] &&
     grep --quiet '|alerts-node|apps.victoriametrics.com/finalizer|1$' "$tmp/state" &&
     [ "$(grep --count "^[a-z]*|$MA_RELEASE|$MA_NAMESPACE|" "$tmp/state")" = 1 ] &&
     grep --quiet "could not clear .*alerts-node" "$tmp/err"; then bounded=1; fi
  if [ "$bounded" = 0 ]; then cat "$tmp/state" "$tmp/err"; fi
  rm -rf "$tmp"
  [ "$bounded" = 1 ] || return 1
}
