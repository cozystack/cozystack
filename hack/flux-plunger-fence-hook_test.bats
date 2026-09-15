#!/usr/bin/env bats
# Behavioural test for flux-plunger's pre-delete hook
# (packages/system/flux-plunger/templates/uninstall-fence-cleanup.yaml), which
# releases the uninstall fence before the chart is removed.
#
# The script is extracted from the rendered chart and RUN against a fake kubectl
# that keeps each HelmRelease's finalizers and suspension owner in files, and a
# fake clock. The fake answers the hook's templates only when they carry the exact
# fence literal, and applies a finalizer removal only when it is the guarded
# test-then-remove at the index that really holds the fence, so a hook that asks
# the wrong question or sends the wrong patch leaves fences behind and fails here.
# The helm-unittest suite in packages/system/flux-plunger/tests matches the
# manifest text instead.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests). The runner
# has no setup/teardown, so each @test builds its own fixture and removes it at the
# end of the body. No EXIT trap: docs/agents/e2e-testing.md bans them in
# hack/*.bats.

CHART=packages/system/flux-plunger
JOB=flux-plunger-uninstall-fence-cleanup
FENCE=flux-plunger.cozystack.io/uninstall-fence

# render_hook <out>
# Extract the hook Job's shell script, its activeDeadlineSeconds and the waiting
# budget the script declares. The non-empty checks are early `return 1`s:
# hack/cozytest.sh rewrites every line matching ^}$ into `return 0` plus `}`,
# helpers included.
render_hook() {
  helm template flux-plunger "$CHART" --namespace cozy-fluxcd \
    | yq eval "select(.kind == \"Job\" and .metadata.name == \"$JOB\") | .spec.template.spec.containers[0].command[2]" - > "$1"
  [ -s "$1" ] || return 1
  DEADLINE=$(helm template flux-plunger "$CHART" --namespace cozy-fluxcd \
    | yq eval "select(.kind == \"Job\" and .metadata.name == \"$JOB\") | .spec.activeDeadlineSeconds" -)
  [ -n "$DEADLINE" ] || return 1
  BUDGET=$(sed -n 's/^ *budget=\([0-9][0-9]*\)$/\1/p' "$1")
  [ -n "$BUDGET" ] || return 1
}

# add_release <state> <ns> <name> <finalizer>...
# One HelmRelease, its finalizers in order, one per line.
add_release() {
  st=$1 ns=$2 name=$3
  shift 3
  : > "$st/$ns@$name.fin"
  for f in "$@"; do
    echo "$f" >> "$st/$ns@$name.fin"
  done
}

# fences_left <state>: how many releases still carry the fence.
fences_left() {
  cat "$1"/*.fin | grep --count --line-regexp "$FENCE"
}

# write_fakes <dir>
# A fake kubectl, sleep and date sharing one simulated clock in $CLOCK (seconds
# since the hook started): sleep advances it, date +%s reads it. kubectl logs
# every call to $KLOG and works on the release state in $STATE:
#   * <ns>@<name>.fin     finalizers, in order;
#   * <ns>@<name>.owned   present when flux-plunger's apply owns spec.suspend,
#                         holding what the ownership template prints;
#   * <ns>@<name>.stalled present when the release is being deleted and
#                         helm-controller never handles it;
#   * <ns>@<name>.vanish  the fence goes (another actor removed it) the first
#                         time the release is read after the listing;
#   * <key>.fail          a count of calls to fail first, for the listing
#                         (list), a release's state read (<ns>@<name>.state)
#                         or its finalizer patch (<ns>@<name>.patch).
# Unsuspended releases are appended to $STATE/unsuspended.
# No line below is a bare column-0 closing brace, so cozytest.sh leaves it intact.
write_fakes() {
  cat > "$1/kubectl" <<'KEOF'
#!/bin/sh
echo "$*" >> "$KLOG"
FENCE=flux-plunger.cozystack.io/uninstall-fence
# In the hook's pod kubectl finds the API server only through the in-cluster
# config, which client-go stops falling back to once a client override such as
# --request-timeout is set: the call then goes to localhost:8080 and fails.
case " $* " in
  *" --request-timeout"*|*" --server"*|*" --token"*)
    echo "The connection to the server localhost:8080 was refused" >&2
    exit 1 ;;
esac
take_fail() {
  [ -s "$1.fail" ] || return 1
  n=$(cat "$1.fail")
  [ "$n" -gt 0 ] || return 1
  echo $((n - 1)) > "$1.fail"
  return 0
  }
ns="" name="" payload="" tmpl="" type="" fm="" all=no seen_hr=no verb=""
while [ $# -gt 0 ]; do
  case "$1" in
    --namespace) ns=$2; shift ;;
    --all-namespaces) all=yes ;;
    --output) tmpl=$2; shift ;;
    --type) type=$2; shift ;;
    --patch) payload=$2; shift ;;
    --field-manager) fm=$2; shift ;;
    --selector) shift ;;
    --*) ;;
    get|patch|scale|wait) [ -n "$verb" ] || verb=$1 ;;
    helmreleases.helm.toolkit.fluxcd.io) seen_hr=yes ;;
    *) if [ "$seen_hr" = yes ] && [ -z "$name" ]; then name=$1; fi ;;
  esac
  shift
done
case "$verb" in
  scale|wait) exit 0 ;;
esac
rel="$STATE/$ns@$name"
if [ "$verb" = get ] && [ -z "$name" ]; then
  if take_fail "$STATE/list"; then echo "fake: the listing was throttled" >&2; exit 1; fi
  case "$tmpl" in *"eq . \"$FENCE\""*) ;; *) exit 0 ;; esac
  for f in "$STATE"/*@*.fin; do
    [ -e "$f" ] || continue
    grep -q -x "$FENCE" "$f" || continue
    r=$(basename "$f" .fin)
    rns=${r%@*}
    if [ "$all" = yes ] || [ "$rns" = "$ns" ]; then printf '%s/%s\n' "$rns" "${r#*@}"; fi
  done
  exit 0
fi
if [ "$verb" = get ]; then
  [ -e "$rel.fin" ] || exit 0
  if [ -e "$rel.vanish" ]; then
    rm -f "$rel.vanish"
    grep -v -x "$FENCE" "$rel.fin" > "$rel.tmp"
    mv "$rel.tmp" "$rel.fin"
  fi
  case "$tmpl" in
    *managedFields*)
      case "$tmpl" in *'eq .manager "flux-plunger"'*) [ -e "$rel.owned" ] && cat "$rel.owned" ;; esac
      exit 0 ;;
    *deletionTimestamp*)
      if take_fail "$rel.state"; then echo "fake: the state read timed out" >&2; exit 1; fi
      [ -e "$rel.stalled" ] && printf 'wait'
      exit 0 ;;
    *'range $i, $f'*)
      case "$tmpl" in
        *"eq \$f \"$FENCE\""*) grep -n -x "$FENCE" "$rel.fin" | head -n 1 | cut -d: -f1 | awk '{ printf "%d", $1 - 1 }' ;;
      esac
      exit 0 ;;
  esac
fi
if [ "$verb" = patch ]; then
  [ -e "$rel.fin" ] || { echo "fake: $ns/$name not found" >&2; exit 1; }
  case "$type" in
    merge)
      if [ "$payload" != '{"spec":{"suspend":false}}' ] || [ "$fm" != flux-plunger ]; then
        echo "fake: unexpected merge patch $payload as $fm" >&2
        exit 1
      fi
      rm -f "$rel.owned"
      echo "$ns/$name" >> "$STATE/unsuspended"
      exit 0 ;;
    json)
      if take_fail "$rel.patch"; then echo "fake: the patch was rejected" >&2; exit 1; fi
      i=$(printf '%s' "$payload" | sed -n 's#^\[{"op":"test","path":"/metadata/finalizers/\([0-9][0-9]*\)","value":"flux-plunger\.cozystack\.io/uninstall-fence"},{"op":"remove","path":"/metadata/finalizers/\1"}\]$#\1#p')
      [ -n "$i" ] || { echo "fake: not a guarded fence removal: $payload" >&2; exit 1; }
      [ "$(sed -n "$((i + 1))p" "$rel.fin")" = "$FENCE" ] || { echo "fake: testing value /metadata/finalizers/$i failed" >&2; exit 1; }
      sed -i.bak "$((i + 1))d" "$rel.fin"
      exit 0 ;;
  esac
fi
echo "fake kubectl: unexpected call" >&2
exit 64
KEOF
  cat > "$1/sleep" <<'SEOF'
#!/bin/sh
now=$(cat "$CLOCK")
echo $((now + $1)) > "$CLOCK"
SEOF
  cat > "$1/date" <<'DEOF'
#!/bin/sh
cat "$CLOCK"
DEOF
  chmod 0755 "$1/kubectl" "$1/sleep" "$1/date"
}

# prepare <dir>: fakes, script, empty state, clock at zero.
prepare() {
  write_fakes "$1"
  render_hook "$1/hook.sh" || return 1
  mkdir "$1/state"
  export KLOG="$1/calls" CLOCK="$1/clock" STATE="$1/state" NAMESPACE=cozy-fluxcd
  echo 0 > "$CLOCK"
  : > "$KLOG"
}

@test "the hook's deadline sits under helm's and its waiting budget under the deadline" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  echo "deadline=${DEADLINE}s budget=${BUDGET}s"
  # helm-controller runs the pre-delete hook under the release's uninstall
  # timeout, 300s by default, and the operator sets none; that clock starts
  # when the hook is created, so the Job's own deadline has to end first.
  [ "$DEADLINE" -lt 300 ]
  # After the waiting budget, each fenced release still needs a few API calls.
  [ $((DEADLINE - BUDGET)) -ge 60 ]
  rm -rf "$tmp"
}

@test "a helm-controller that never answers costs the waiting budget once, whatever the number of releases" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  # Twenty fenced releases across tenant namespaces, all being deleted, none of
  # which helm-controller will ever finish uninstalling. Half still carry
  # helm-controller's finalizer ahead of the fence.
  i=1
  while [ "$i" -le 20 ]; do
    if [ $((i % 2)) -eq 0 ]; then
      add_release "$STATE" "tenant-$i" "app-$i" finalizers.fluxcd.io "$FENCE"
    else
      add_release "$STATE" "tenant-$i" "app-$i" "$FENCE"
    fi
    touch "$STATE/tenant-$i@app-$i.stalled"
    i=$((i + 1))
  done

  status=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2>&1 || status=$?

  elapsed=$(cat "$CLOCK")
  cat "$tmp/out"
  echo "status=$status elapsed=${elapsed}s budget=${BUDGET}s left=$(fences_left "$STATE")"
  [ "$status" -eq 0 ]
  [ "$(fences_left "$STATE")" -eq 0 ]
  [ "$(cat "$STATE"/*.fin | grep --count --line-regexp finalizers.fluxcd.io)" -eq 10 ]
  [ "$elapsed" -le "$BUDGET" ]
  [ "$elapsed" -ge $((BUDGET - 2)) ]
  grep -q -x 'released 20 of 20 fenced HelmReleases' "$tmp/out"
  rm -rf "$tmp"
}

@test "the hook lifts only flux-plunger's own suspension and removes only the fence, at the index it holds" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  add_release "$STATE" tenant-a app-a example.com/keep "$FENCE" finalizers.fluxcd.io
  echo yes > "$STATE/tenant-a@app-a.owned"
  add_release "$STATE" tenant-b app-b "$FENCE"
  # Two managedFields entries would print the ownership mark twice.
  add_release "$STATE" tenant-c app-c example.com/one example.com/two "$FENCE"
  echo yesyes > "$STATE/tenant-c@app-c.owned"
  # Someone else removes this fence between the listing and the hook's next read.
  add_release "$STATE" tenant-d app-d "$FENCE" example.com/keep
  touch "$STATE/tenant-d@app-d.vanish"

  status=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2>&1 || status=$?

  cat "$tmp/out"
  echo "status=$status unsuspended=$(tr '\n' ' ' < "$STATE/unsuspended" 2>/dev/null)"
  [ "$status" -eq 0 ]
  [ "$(fences_left "$STATE")" -eq 0 ]
  [ "$(sort "$STATE/unsuspended" | tr '\n' ' ')" = "tenant-a/app-a tenant-c/app-c " ]
  [ "$(tr '\n' ' ' < "$STATE/tenant-a@app-a.fin")" = "example.com/keep finalizers.fluxcd.io " ]
  [ "$(tr '\n' ' ' < "$STATE/tenant-c@app-c.fin")" = "example.com/one example.com/two " ]
  [ "$(tr '\n' ' ' < "$STATE/tenant-d@app-d.fin")" = "example.com/keep " ]
  grep -q -x 'released 4 of 4 fenced HelmReleases' "$tmp/out"
  [ "$(cat "$CLOCK")" -eq 0 ]
  rm -rf "$tmp"
}

@test "transient API failures are retried rather than taken as an answer" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  echo 2 > "$STATE/list.fail"
  add_release "$STATE" tenant-a app-a "$FENCE"
  echo 2 > "$STATE/tenant-a@app-a.patch.fail"
  # A release helm-controller has handled, whose state read times out three
  # times first: a failed read is not a "done".
  add_release "$STATE" tenant-b app-b "$FENCE"
  echo 3 > "$STATE/tenant-b@app-b.state.fail"

  status=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2>&1 || status=$?

  cat "$tmp/out"
  echo "status=$status elapsed=$(cat "$CLOCK")s"
  [ "$status" -eq 0 ]
  [ "$(fences_left "$STATE")" -eq 0 ]
  [ "$(grep 'tenant-b' "$KLOG" | grep --count 'deletionTimestamp')" -ge 4 ]
  grep -q -x 'released 2 of 2 fenced HelmReleases' "$tmp/out"
  rm -rf "$tmp"
}

@test "a fence the hook cannot remove fails the hook and is named" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  add_release "$STATE" tenant-a app-a "$FENCE"
  echo 9999 > "$STATE/tenant-a@app-a.patch.fail"
  add_release "$STATE" tenant-b app-b "$FENCE"

  status=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2>&1 || status=$?

  cat "$tmp/out"
  echo "status=$status elapsed=$(cat "$CLOCK")s"
  [ "$status" -ne 0 ]
  grep -q '^NOT released tenant-a/app-a' "$tmp/out"
  # `! grep` would not trip set -e.
  if grep -q "released $FENCE from tenant-a/app-a" "$tmp/out"; then exit 1; fi
  grep -q -x 'released 1 of 2 fenced HelmReleases' "$tmp/out"
  [ "$(fences_left "$STATE")" -eq 1 ]
  [ "$(cat "$CLOCK")" -le $((BUDGET + 10)) ]
  rm -rf "$tmp"
}

@test "a listing that never succeeds fails the hook instead of reading as no fences" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  echo 9999 > "$STATE/list.fail"
  add_release "$STATE" tenant-a app-a "$FENCE"

  status=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2>&1 || status=$?

  cat "$tmp/out"
  echo "status=$status elapsed=$(cat "$CLOCK")s"
  [ "$status" -ne 0 ]
  grep -q 'could not list' "$tmp/out"
  [ "$(cat "$CLOCK")" -le $((BUDGET + 10)) ]
  rm -rf "$tmp"
}
