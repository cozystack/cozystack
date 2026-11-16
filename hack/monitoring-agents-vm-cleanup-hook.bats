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
# kind|release-name|release-namespace|namespace|name, where the two release
# fields are the Helm annotations (empty when Helm does not own the object).
# `get` prints this kind's objects in the hook's jsonpath format, or fails for a
# kind listed in $MISSING_KINDS as kubectl does when the CRD is gone. `delete`
# removes the object only when $OPERATOR_LIVE is 1, standing for an operator
# that finalizes it; otherwise the object stays, Terminating. `patch` removes it
# only when the payload actually clears the finalizers. Every call goes to $KLOG.
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
drop() {
  awk -F'|' -v k="$1" -v ns="$2" -v n="$3" '!($1 == k && $4 == ns && $5 == n)' "$STATE" > "$STATE.new"
  mv "$STATE.new" "$STATE"; }
case "$1" in
  get)
    kind=${2%%.*}
    case " ${MISSING_KINDS:-} " in
      *" $kind "*)
        echo "error: the server doesn't have a resource type \"$kind\"" >&2
        exit 1 ;;
    esac
    awk -F'|' -v k="$kind" '$1 == k { print $2 "|" $3 "|" $4 "|" $5 }' "$STATE"
    exit 0 ;;
  -n)
    ns=$2 verb=$3 kind=${4%%.*} name=$5
    case "$verb" in
      delete)
        [ "${OPERATOR_LIVE:-0}" = 1 ] && drop "$kind" "$ns" "$name"
        echo "$kind \"$name\" deleted"
        exit 0 ;;
      patch)
        case "$*" in
          *'"finalizers":null'*) drop "$kind" "$ns" "$name" ;;
        esac
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
vmagents|$MA_RELEASE|$MA_NAMESPACE|$MA_NAMESPACE|vmagent
vmrules|$MA_RELEASE|$MA_NAMESPACE|$MA_NAMESPACE|alerts-node
vmservicescrapes|$MA_RELEASE|$MA_NAMESPACE|cozy-victoria-metrics-operator|operator
vmrules|other|$MA_NAMESPACE|$MA_NAMESPACE|foreign-rule
vmrules|$MA_RELEASE|other-namespace|$MA_NAMESPACE|foreign-ns-rule
vmservicescrapes|||$MA_NAMESPACE|unmanaged-scrape
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
  printf 'vmrules|other|%s|%s|foreign-rule\nvmrules|%s|other-namespace|%s|foreign-ns-rule\n' \
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
