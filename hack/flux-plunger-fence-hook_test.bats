#!/usr/bin/env bats
# Behavioural test for flux-plunger's pre-delete hook
# (packages/system/flux-plunger/templates/uninstall-fence-cleanup.yaml), the
# backstop that releases what flux-plunger's own shutdown drain could not.
#
# The script is extracted from the rendered chart and RUN against a fake kubectl
# that keeps each HelmRelease's finalizers, suspension owner, resourceVersion,
# generation and observed generation in files, and a fake clock. The fake answers
# the hook's templates only when they ask the right question (the exact fence
# literal, the f:suspend field of flux-plunger's Apply entry, the exact
# "helm-controller has not handled it" predicate), lists a release once per fence
# entry as the real template does unless the template collapses them, bumps a
# release's generation when its suspension is lifted as the apiserver does, lifts
# a suspension only as a JSON patch preconditioned on the resourceVersion and uid
# it read and never one another manager also holds, refuses kubectl apply, and
# applies a finalizer removal only when it is the guarded test-then-remove at an
# index that holds the fence. The helm-unittest
# suites in packages/system/flux-plunger/tests match the manifest text instead.
#
# CI runs this file under Bats through `make bats-unit-tests`. It also remains
# compatible with the legacy `hack/cozytest.sh` translator, so each @test builds
# its own fixture and removes it at the end of the body instead of using
# setup()/teardown(). No EXIT trap: docs/agents/e2e-testing.md bans them in
# hack/*.bats.
#
# Run with: bats hack/flux-plunger-fence-hook_test.bats

load test_helper

CHART=packages/system/flux-plunger
JOB=flux-plunger-uninstall-fence-cleanup
FENCE=flux-plunger.cozystack.io/uninstall-fence
HELMFIN=finalizers.fluxcd.io

# render_hook <out> [helm args]
# Extract the hook Job's shell script and activeDeadlineSeconds, the script's pod
# wait, retry window, selector and Deployment name, and the Deployment's grace
# period, name and pod labels, and the hook Role's resourceNames. The non-empty
# checks are early `return 1`s: hack/cozytest.sh rewrites every line matching ^}$
# into `return 0` plus `}`, helpers included.
render_hook() {
  out=$1
  shift
  helm template flux-plunger "$CHART" --namespace cozy-fluxcd "$@" > "$out.all"
  yq eval "select(.kind == \"Job\" and .metadata.name == \"$JOB\") | .spec.template.spec.containers[0].command[2]" "$out.all" > "$out"
  [ -s "$out" ] || return 1
  DEADLINE=$(yq eval "select(.kind == \"Job\" and .metadata.name == \"$JOB\") | .spec.activeDeadlineSeconds" "$out.all")
  GRACE=$(yq eval 'select(.kind == "Deployment") | .spec.template.spec.terminationGracePeriodSeconds' "$out.all")
  DEPLOY_NAME=$(yq eval 'select(.kind == "Deployment") | .metadata.name' "$out.all")
  ROLE_DEPLOY=$(yq eval "select(.kind == \"Role\" and .metadata.name == \"$JOB\") | .rules[0].resourceNames[0]" "$out.all")
  POD_LABELS=$(yq eval 'select(.kind == "Deployment") | .spec.template.metadata.labels | to_entries | map(.key + "=" + .value) | join(",")' "$out.all")
  POD_WAIT=$(sed -n 's/^ *pod_wait=\([0-9][0-9]*\)$/\1/p' "$out")
  WINDOW=$(sed -n 's/.*retry_until=\$(( \$(date +%s) + \([0-9][0-9]*\) )).*/\1/p' "$out")
  SCRIPT_DEPLOY=$(sed -n 's/^ *deploy=\([a-z0-9-]*\)$/\1/p' "$out")
  POD_SELECTOR=$(sed -n 's/.*--selector \([^ ]*\).*/\1/p' "$out" | sort -u)
  for v in "$DEADLINE" "$GRACE" "$DEPLOY_NAME" "$ROLE_DEPLOY" "$POD_LABELS" "$POD_WAIT" "$WINDOW" "$SCRIPT_DEPLOY" "$POD_SELECTOR"; do
    [ -n "$v" ] || return 1
  done
}

# add_release <state> <ns> <name> <finalizer>...
# One HelmRelease, its finalizers in order, one per line, at generation 1 and
# observed, resourceVersion 1, suspended.
add_release() {
  st=$1 ns=$2 name=$3
  shift 3
  : > "$st/$ns@$name.fin"
  for f in "$@"; do
    echo "$f" >> "$st/$ns@$name.fin"
  done
  echo 1 > "$st/$ns@$name.gen"
  echo 1 > "$st/$ns@$name.obs"
  echo 1 > "$st/$ns@$name.rv"
  touch "$st/$ns@$name.suspended"
}

# fences_left <state>: how many fence entries remain across all releases.
fences_left() {
  cat "$1"/*.fin 2>/dev/null | grep --count --line-regexp "$FENCE"
}

# write_fakes <dir>
# A fake kubectl, sleep and date sharing one simulated clock in $CLOCK (seconds
# since the hook started): sleep advances it, date +%s reads it. kubectl logs
# every call to $KLOG and works on the release state in $STATE:
#   * <ns>@<name>.fin        finalizers, in order;
#   * <ns>@<name>.gen/.obs   metadata.generation and status.observedGeneration;
#                            lifting a suspension bumps the generation;
#   * <ns>@<name>.rv         resourceVersion, bumped by every write;
#   * <ns>@<name>.suspended  spec.suspend is true;
#   * <ns>@<name>.deleting   the release is being deleted;
#   * <ns>@<name>.owned      flux-plunger's Apply entry owns spec.suspend,
#                            holding what a correct ownership template prints;
#   * <ns>@<name>.coowned    another manager also owns spec.suspend at true;
#                            flux-plunger's own path leaves such a suspension;
#   * <ns>@<name>.applyonly  flux-plunger has an Apply entry that does not own
#                            spec.suspend;
#   * <ns>@<name>.updateowned  flux-plunger owns spec.suspend through an Update
#                            entry, as an earlier flux-plunger's merge patch left
#                            it; the controller does not take that one as its own;
#   * <ns>@<name>.vanish     the release is deleted the first time it is read
#                            after the listing; reading it then is NotFound
#                            unless --ignore-not-found is given;
#   * <ns>@<name>.readd      something puts the fence back after every removal,
#                            as a flux-plunger still running would;
#   * <ns>@<name>.latefence  the release gains the fence right after the first
#                            listing, as one a running flux-plunger fences would;
#   * deploy.absent          the flux-plunger Deployment does not exist;
#   * pods.stuck             flux-plunger's pod never goes: the wait runs to its
#                            --timeout on the clock (a negative one, as kubectl
#                            reads it, for a week) and fails, and the pod lists;
#   * scale.slow             seconds a successful scale-down takes;
#   * <key>.fail             a count of calls to fail first, for the Deployment
#                            read (deploy), the scale-down (scale), the pod wait
#                            for a reason other than pods (wait), the listing
#                            (list), a listing after the first (relist), the
#                            pod listing (pods), a release's ownership read
#                            (<ns>@<name>.ownedread), unsuspend
#                            (<ns>@<name>.apply), unsuspend racing another
#                            write (<ns>@<name>.applyconflict) or finalizer patch
#                            (<ns>@<name>.patch);
#   * list.partial.fail      a count of listings that print what they have, then
#                            fail, as kubectl does on a partial error.
# Releases whose suspension was lifted are appended to $STATE/unsuspended.
# No line below is a bare column-0 closing brace, so cozytest.sh leaves it intact.
write_fakes() {
  cat > "$1/kubectl" <<'KEOF'
#!/bin/sh
echo "$*" >> "$KLOG"
FENCE=flux-plunger.cozystack.io/uninstall-fence
HELMFIN=finalizers.fluxcd.io
# The one predicate that tells a release helm-controller has not yet handled in
# its unsuspended form: being deleted, no longer held by its finalizer, and its
# latest generation not yet observed.
UNHANDLED='{{if .metadata.deletionTimestamp}}{{$held := false}}{{range .metadata.finalizers}}{{if eq . "finalizers.fluxcd.io"}}{{$held = true}}{{end}}{{end}}{{if and (not $held) (ne (printf "%v" .status.observedGeneration) (printf "%v" .metadata.generation))}}'
# In the hook's pod kubectl finds the API server only through the in-cluster
# config, which client-go stops falling back to once a client override such as
# --request-timeout is set: the call then goes to localhost:8080 and fails.
case " $* " in
  *" --request-timeout"*|*" --server="*|*" --server "*|*" --token"*)
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
bump_rv() {
  echo $(( $(cat "$1.rv") + 1 )) > "$1.rv"
  }
ns="" name="" payload="" tmpl="" type="" fm="" all=no kind="" verb="" ignore=no timeout="" for="" ssa=no file=""
while [ $# -gt 0 ]; do
  case "$1" in
    --namespace) ns=$2; shift ;;
    --all-namespaces) all=yes ;;
    --output) tmpl=$2; shift ;;
    --type) type=$2; shift ;;
    --patch) payload=$2; shift ;;
    --field-manager) fm=$2; shift ;;
    --filename) file=$2; shift ;;
    --selector) shift ;;
    --ignore-not-found) ignore=yes ;;
    --server-side) ssa=yes ;;
    --timeout=*) timeout=${1#--timeout=}; timeout=${timeout%s} ;;
    --for=*) for=${1#--for=} ;;
    --*) ;;
    get|patch|scale|wait|apply) [ -n "$verb" ] || verb=$1 ;;
    helmreleases.helm.toolkit.fluxcd.io) kind=hr ;;
    deployment) kind=deployment ;;
    pod) kind=pod ;;
    *) if [ -n "$kind" ] && [ -z "$name" ]; then name=$1; fi ;;
  esac
  shift
done
case "$verb" in
  scale)
    if take_fail "$STATE/scale"; then echo "fake: the scale-down was rejected" >&2; exit 1; fi
    if [ -s "$STATE/scale.slow" ]; then echo $(( $(cat "$CLOCK") + $(cat "$STATE/scale.slow") )) > "$CLOCK"; fi
    echo "deployment.apps/$name scaled"
    exit 0 ;;
  wait)
    if [ "$for" != delete ]; then echo "fake: waiting on '$for', not on the pods being gone" >&2; exit 64; fi
    if take_fail "$STATE/wait"; then echo "error: watch closed before the condition was met" >&2; exit 1; fi
    if [ -e "$STATE/pods.stuck" ]; then
      now=$(cat "$CLOCK")
      # kubectl wait: zero checks once, a negative timeout waits for a week.
      [ "$timeout" -ge 0 ] || timeout=604800
      echo $((now + timeout)) > "$CLOCK"
      echo "error: timed out waiting for the condition on pods/flux-plunger-0" >&2
      exit 1
    fi
    exit 0 ;;
esac
if [ "$kind" = pod ] && [ "$verb" = get ]; then
  if take_fail "$STATE/pods"; then echo "fake: the pod listing timed out" >&2; exit 1; fi
  [ -e "$STATE/pods.stuck" ] && echo "pod/flux-plunger-0"
  exit 0
fi
if [ "$kind" = deployment ] && [ "$verb" = get ]; then
  if take_fail "$STATE/deploy"; then echo "fake: the Deployment read timed out" >&2; exit 1; fi
  [ -e "$STATE/deploy.absent" ] || echo "deployment.apps/$name"
  exit 0
fi
if [ "$verb" = apply ]; then
  # kubectl apply --server-side migrates every field kubectl-client-side-apply
  # owns to the applying manager and then re-applies, so a minimal manifest strips
  # those fields from a release created with a client-side kubectl apply.
  echo "fake: kubectl apply takes over and strips client-side-apply fields" >&2
  exit 64
fi
rel="$STATE/$ns@$name"
if [ "$verb" = get ] && [ -z "$name" ]; then
  if take_fail "$STATE/list"; then echo "fake: the listing was throttled" >&2; exit 1; fi
  if [ -e "$STATE/listed" ] && take_fail "$STATE/relist"; then echo "fake: the second listing was throttled" >&2; exit 1; fi
  partial=no
  if take_fail "$STATE/list.partial"; then partial=yes; fi
  case "$tmpl" in *"eq . \"$FENCE\""*) ;; *) exit 0 ;; esac
  for f in "$STATE"/*@*.fin; do
    [ -e "$f" ] || continue
    r=$(basename "$f" .fin)
    rns=${r%@*}
    if [ "$all" = yes ] || [ "$rns" = "$ns" ]; then
      case "$tmpl" in
        *'{{if $fenced}}'*) grep -q -x "$FENCE" "$f" && printf '%s/%s\n' "$rns" "${r#*@}" ;;
        *) grep -x "$FENCE" "$f" | while read -r _; do printf '%s/%s\n' "$rns" "${r#*@}"; done ;;
      esac
    fi
  done
  for l in "$STATE"/*@*.latefence; do
    [ -e "$l" ] || continue
    echo "$FENCE" >> "${l%.latefence}.fin"
    rm -f "$l"
  done
  if [ "$partial" = yes ]; then echo "fake: an error occurred partway through the listing" >&2; exit 1; fi
  touch "$STATE/listed"
  exit 0
fi
if [ "$verb" = get ]; then
  if [ -e "$rel.vanish" ]; then
    rm -f "$rel.vanish" "$rel.fin"
  fi
  if [ ! -e "$rel.fin" ]; then
    [ "$ignore" = yes ] && exit 0
    echo "Error from server (NotFound): helmreleases.helm.toolkit.fluxcd.io \"$name\" not found" >&2
    exit 1
  fi
  case "$tmpl" in
    *managedFields*)
      if take_fail "$rel.ownedread"; then
        echo "Error executing template: an error blob on stdout"
        exit 1
      fi
      case "$tmpl" in *'.metadata.resourceVersion}} {{.metadata.uid}}'*) printf '%s uid-%s ' "$(cat "$rel.rv")" "$name" ;; esac
      case "$tmpl" in *'{{if .spec.suspend}}'*) [ -e "$rel.suspended" ] || exit 0 ;; esac
      case "$tmpl" in *'eq .manager "flux-plunger"'*) ;; *) exit 0 ;; esac
      [ -e "$rel.owned" ] && cat "$rel.owned"
      case "$tmpl" in *'(eq .operation "Apply")'*) ;; *) [ -e "$rel.updateowned" ] && echo yes ;; esac
      case "$tmpl" in *'"f:suspend"'*) ;; *) [ -e "$rel.applyonly" ] && echo yes ;; esac
      # Another manager's entry also owning spec.suspend.
      case "$tmpl" in *'ne .manager "flux-plunger"'*) [ -e "$rel.coowned" ] && echo other ;; esac
      exit 0 ;;
    *deletionTimestamp*)
      case "$tmpl" in *"$UNHANDLED"*) ;; *) exit 0 ;; esac
      [ -e "$rel.deleting" ] || exit 0
      grep -q -x "$HELMFIN" "$rel.fin" && exit 0
      [ "$(cat "$rel.gen")" = "$(cat "$rel.obs")" ] && exit 0
      printf 'unhandled'
      exit 0 ;;
    *'range $i, $f'*)
      case "$tmpl" in
        *"eq \$f \"$FENCE\""*) grep -n -x "$FENCE" "$rel.fin" | cut -d: -f1 | awk '{ printf "%d ", $1 - 1 }' ;;
      esac
      exit 0 ;;
  esac
fi
if [ "$verb" = patch ]; then
  [ -e "$rel.fin" ] || { echo "fake: $ns/$name not found" >&2; exit 1; }
  case "$type" in
    json)
      case "$payload" in
        *'"path":"/spec/suspend"'*)
          if take_fail "$rel.apply"; then echo "fake: the unsuspend was rejected" >&2; exit 1; fi
          if take_fail "$rel.applyconflict"; then bump_rv "$rel"; fi
          if [ "$fm" != flux-plunger ]; then echo "fake: unsuspend not written as flux-plunger" >&2; exit 64; fi
          want="[{\"op\":\"test\",\"path\":\"/metadata/resourceVersion\",\"value\":\"$(cat "$rel.rv")\"},{\"op\":\"test\",\"path\":\"/metadata/uid\",\"value\":\"uid-$name\"},{\"op\":\"replace\",\"path\":\"/spec/suspend\",\"value\":false}]"
          if [ "$payload" != "$want" ]; then echo "fake: testing value /metadata/resourceVersion failed or not a preconditioned unsuspend: $payload" >&2; exit 1; fi
          if [ -e "$rel.coowned" ]; then echo "fake: cleared a suspension another manager also holds" >&2; exit 64; fi
          rm -f "$rel.owned" "$rel.applyonly" "$rel.suspended"
          echo $(( $(cat "$rel.gen") + 1 )) > "$rel.gen"
          echo "$ns/$name" >> "$STATE/unsuspended"
          bump_rv "$rel"
          exit 0 ;;
      esac
      if take_fail "$rel.patch"; then echo "fake: the patch was rejected" >&2; exit 1; fi
      i=$(printf '%s' "$payload" | sed -n 's#^\[{"op":"test","path":"/metadata/finalizers/\([0-9][0-9]*\)","value":"flux-plunger\.cozystack\.io/uninstall-fence"},{"op":"remove","path":"/metadata/finalizers/\1"}\]$#\1#p')
      [ -n "$i" ] || { echo "fake: not a guarded fence removal: $payload" >&2; exit 1; }
      [ "$(sed -n "$((i + 1))p" "$rel.fin")" = "$FENCE" ] || { echo "fake: testing value /metadata/finalizers/$i failed" >&2; exit 1; }
      sed -i.bak "$((i + 1))d" "$rel.fin"
      [ -e "$rel.readd" ] && echo "$FENCE" >> "$rel.fin"
      bump_rv "$rel"
      now=$(cat "$CLOCK")
      echo $((now + 1)) > "$CLOCK"
      exit 0 ;;
  esac
  echo "fake: a $type patch; spec.suspend is lifted only by the guarded JSON patch" >&2
  exit 64
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

# run_hook <dir>: run the script, its output in <dir>/out, its status in $status.
run_hook() {
  status=0
  PATH="$1:$PATH" sh "$1/hook.sh" > "$1/out" 2>&1 || status=$?
  cat "$1/out"
  echo "status=$status elapsed=$(cat "$CLOCK")s"
}

@test "the hook waits for flux-plunger's own drain, fits under helm's bound, and stops the Deployment the chart deploys" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  echo "deadline=${DEADLINE}s grace=${GRACE}s pod-wait=${POD_WAIT}s window=${WINDOW}s deploy=$SCRIPT_DEPLOY/$DEPLOY_NAME/$ROLE_DEPLOY selector=$POD_SELECTOR labels=$POD_LABELS"
  # helm-controller runs the pre-delete hook under the release's uninstall
  # timeout, 300s by default, and the operator sets none; that clock starts
  # when the hook is created, so the Job's own deadline has to end first.
  [ "$DEADLINE" -lt 300 ]
  # Retries stop a minute before the Job is killed, the pod wait covers the
  # pod's grace period, and fits inside the retry window.
  [ $((DEADLINE - WINDOW)) -ge 60 ]
  [ "$POD_WAIT" -gt "$GRACE" ]
  [ "$POD_WAIT" -lt "$WINDOW" ]
  # The wait must find flux-plunger's pod: kubectl wait --for=delete succeeds at
  # once on a selector that matches nothing.
  [ "$POD_SELECTOR" = "$POD_LABELS" ]
  # A name the chart does not deploy reads as a Deployment already gone.
  [ "$SCRIPT_DEPLOY" = "$DEPLOY_NAME" ]
  [ "$ROLE_DEPLOY" = "$DEPLOY_NAME" ]
  rm -rf "$tmp"
}

@test "at the largest grace period the chart accepts the hook still fits under helm's bound" {
  tmp=$(mktemp -d)
  render_hook "$tmp/hook.sh" --set shutdownGracePeriodSeconds=140
  echo "deadline=$DEADLINE pod-wait=$POD_WAIT window=$WINDOW"
  [ "$DEADLINE" -lt 300 ]
  [ $((DEADLINE - WINDOW)) -ge 60 ]
  [ "$POD_WAIT" -gt 140 ]
  [ "$POD_WAIT" -lt "$WINDOW" ]
  rm -rf "$tmp"
}

@test "the hook lifts only a suspension flux-plunger alone holds and removes only the fence, at the index it holds" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  add_release "$STATE" tenant-a app-a example.com/keep "$FENCE" "$HELMFIN"
  echo yes > "$STATE/tenant-a@app-a.owned"
  add_release "$STATE" tenant-b app-b "$FENCE"
  # Two managedFields entries would print the ownership mark twice.
  add_release "$STATE" tenant-c app-c example.com/one example.com/two "$FENCE"
  echo yesyes > "$STATE/tenant-c@app-c.owned"
  # Someone else removes this fence between the listing and the hook's next read.
  add_release "$STATE" tenant-d app-d "$FENCE" example.com/keep
  touch "$STATE/tenant-d@app-d.vanish"
  # flux-plunger applied to this release, but its entry does not own spec.suspend.
  add_release "$STATE" tenant-e app-e "$FENCE"
  touch "$STATE/tenant-e@app-e.applyonly"
  # A successful read that prints something other than the ownership mark.
  add_release "$STATE" tenant-f app-f "$FENCE"
  echo unrelated > "$STATE/tenant-f@app-f.owned"
  # The apiserver accepts a finalizer listed twice; both copies go, and the
  # release is counted once.
  add_release "$STATE" tenant-g app-g "$FENCE" example.com/keep "$FENCE"
  # Another manager also holds spec.suspend at true: its suspension stands, as
  # the controller's relinquish leaves it.
  add_release "$STATE" tenant-h app-h "$FENCE"
  echo yes > "$STATE/tenant-h@app-h.owned"
  touch "$STATE/tenant-h@app-h.coowned"
  # flux-plunger owns spec.suspend at false, as after its own unsuspend: there is
  # nothing to lift, and no write.
  add_release "$STATE" tenant-i app-i "$FENCE"
  echo yes > "$STATE/tenant-i@app-i.owned"
  rm "$STATE/tenant-i@app-i.suspended"
  # A suspension an earlier flux-plunger left through a merge patch is not one
  # the controller takes as its own, so the hook leaves it too.
  add_release "$STATE" tenant-j app-j "$FENCE"
  touch "$STATE/tenant-j@app-j.updateowned"

  run_hook "$tmp"

  echo "unsuspended=$(tr '\n' ' ' < "$STATE/unsuspended" 2>/dev/null)"
  [ "$status" -eq 0 ]
  [ "$(fences_left "$STATE")" -eq 0 ]
  [ "$(sort "$STATE/unsuspended" | tr '\n' ' ')" = "tenant-a/app-a tenant-c/app-c " ]
  [ -e "$STATE/tenant-h@app-h.suspended" ]
  [ -e "$STATE/tenant-j@app-j.suspended" ]
  [ "$(tr '\n' ' ' < "$STATE/tenant-a@app-a.fin")" = "example.com/keep $HELMFIN " ]
  [ "$(tr '\n' ' ' < "$STATE/tenant-c@app-c.fin")" = "example.com/one example.com/two " ]
  [ "$(tr '\n' ' ' < "$STATE/tenant-g@app-g.fin")" = "example.com/keep " ]
  grep -q -x 'released 10 of 10 fenced HelmReleases, 0 without an uninstall' "$tmp/out"
  # Nothing the fake refused, and the pod wait asked the question it answers.
  if grep -q -e '^fake:' -e 'the wait for flux-plunger pods to go failed' "$tmp/out"; then
    echo "FAIL: the hook made a call the fake refused"
    false
  fi
  rm -rf "$tmp"
}

@test "a release deleted before helm-controller handled it is released, and named as deleted without an uninstall" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  # helm-controller dropped its finalizer on the suspended delete; the fence is
  # all that holds it, and flux-plunger's drain did not get it through. Lifting
  # the suspension bumps the generation, which helm-controller has not observed.
  add_release "$STATE" tenant-a app-a "$FENCE"
  touch "$STATE/tenant-a@app-a.deleting"
  echo yes > "$STATE/tenant-a@app-a.owned"
  # Being deleted, but helm-controller's finalizer still holds it: helm-controller
  # will run the uninstall once the fence is gone.
  add_release "$STATE" tenant-b app-b "$HELMFIN" "$FENCE"
  touch "$STATE/tenant-b@app-b.deleting"
  echo yes > "$STATE/tenant-b@app-b.owned"
  # Being deleted, helm-controller has handled its current generation.
  add_release "$STATE" tenant-c app-c "$FENCE"
  touch "$STATE/tenant-c@app-c.deleting"
  # Its uninstall completes mid-sweep, and the release is gone when read.
  add_release "$STATE" tenant-d app-d "$FENCE"
  touch "$STATE/tenant-d@app-d.vanish"

  run_hook "$tmp"

  [ "$status" -eq 0 ]
  [ "$(fences_left "$STATE")" -eq 0 ]
  grep -q -x "released $FENCE from tenant-a/app-a without an uninstall: helm-controller had not handled it" "$tmp/out"
  grep -q -x "released $FENCE from tenant-b/app-b" "$tmp/out"
  grep -q -x "released $FENCE from tenant-c/app-c" "$tmp/out"
  grep -q -x "released $FENCE from tenant-d/app-d" "$tmp/out"
  grep -q -x 'released 4 of 4 fenced HelmReleases, 1 without an uninstall' "$tmp/out"
  rm -rf "$tmp"
}

@test "transient API failures are retried, and a failed attempt's output is not taken as an answer" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  echo 2 > "$STATE/deploy.fail"
  echo 2 > "$STATE/scale.fail"
  echo 2 > "$STATE/list.fail"
  # Two listings that print what they have and then fail: their output must
  # not be added to the one that succeeds.
  echo 2 > "$STATE/list.partial.fail"
  add_release "$STATE" tenant-a app-a "$FENCE"
  echo 2 > "$STATE/tenant-a@app-a.patch.fail"
  # Not flux-plunger's suspension; a failed ownership read prints an error blob
  # to stdout first.
  add_release "$STATE" tenant-b app-b "$FENCE"
  echo 2 > "$STATE/tenant-b@app-b.ownedread.fail"
  add_release "$STATE" tenant-d app-d "$FENCE"
  echo yes > "$STATE/tenant-d@app-d.owned"
  echo 2 > "$STATE/tenant-d@app-d.apply.fail"
  # A write lands between the ownership read and the unsuspend: the patch is
  # rejected on its resourceVersion, and the decision is read again.
  add_release "$STATE" tenant-e app-e "$FENCE"
  echo yes > "$STATE/tenant-e@app-e.owned"
  echo 1 > "$STATE/tenant-e@app-e.applyconflict.fail"

  run_hook "$tmp"

  [ "$status" -eq 0 ]
  [ "$(fences_left "$STATE")" -eq 0 ]
  [ "$(sort "$STATE/unsuspended" | tr '\n' ' ')" = "tenant-d/app-d tenant-e/app-e " ]
  [ "$(grep --count "released $FENCE from" "$tmp/out")" -eq 4 ]
  grep -q -x 'released 4 of 4 fenced HelmReleases, 0 without an uninstall' "$tmp/out"
  rm -rf "$tmp"
}

@test "a release that cannot be read, unsuspended or unfenced fails the hook, keeps its fence and is named" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  add_release "$STATE" tenant-a app-a "$FENCE"
  echo 9999 > "$STATE/tenant-a@app-a.patch.fail"
  add_release "$STATE" tenant-b app-b "$FENCE"
  echo 9999 > "$STATE/tenant-b@app-b.ownedread.fail"
  # Its suspension is flux-plunger's and cannot be lifted: without the fence a
  # deleted release that is still suspended is removed without an uninstall.
  add_release "$STATE" tenant-c app-c "$FENCE"
  echo yes > "$STATE/tenant-c@app-c.owned"
  echo 9999 > "$STATE/tenant-c@app-c.apply.fail"
  add_release "$STATE" tenant-d app-d "$FENCE"

  run_hook "$tmp"

  [ "$status" -ne 0 ]
  grep -q '^NOT released tenant-a/app-a' "$tmp/out"
  grep -q '^NOT released tenant-b/app-b' "$tmp/out"
  grep -q '^NOT released tenant-c/app-c' "$tmp/out"
  if grep -q "released $FENCE from tenant-[abc]/" "$tmp/out"; then
    echo "FAIL: a release left fenced was reported released"
    false
  fi
  grep -q -x 'released 1 of 4 fenced HelmReleases, 0 without an uninstall' "$tmp/out"
  [ "$(fences_left "$STATE")" -eq 3 ]
  [ -e "$STATE/tenant-c@app-c.owned" ]
  [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
  rm -rf "$tmp"
}

@test "a fence that keeps coming back fails the hook inside its deadline" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  add_release "$STATE" tenant-a app-a "$FENCE"
  touch "$STATE/tenant-a@app-a.readd"

  run_hook "$tmp"

  [ "$status" -ne 0 ]
  grep -q '^NOT released tenant-a/app-a' "$tmp/out"
  [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
  rm -rf "$tmp"
}

@test "a listing that never succeeds fails the hook instead of reading as no fences" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  echo 9999 > "$STATE/list.fail"
  add_release "$STATE" tenant-a app-a "$FENCE"

  run_hook "$tmp"

  [ "$status" -ne 0 ]
  grep -q 'could not list' "$tmp/out"
  [ "$(fences_left "$STATE")" -eq 1 ]
  [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
  rm -rf "$tmp"
}

@test "a Deployment that cannot be read or scaled down fails the hook before any fence is touched" {
  for knob in deploy scale; do
    tmp=$(mktemp -d)
    prepare "$tmp"
    # flux-plunger may still be running and would fence behind a sweep, and a
    # sweep that ran anyway would leave the retried uninstall nothing to
    # converge on.
    echo 9999 > "$STATE/$knob.fail"
    add_release "$STATE" tenant-a app-a "$FENCE"
    echo yes > "$STATE/tenant-a@app-a.owned"

    run_hook "$tmp"

    [ "$status" -ne 0 ]
    grep -q 'no fence was touched' "$tmp/out"
    [ "$(fences_left "$STATE")" -eq 1 ]
    [ -e "$STATE/tenant-a@app-a.owned" ]
    if grep -q 'helmreleases' "$KLOG"; then
      echo "FAIL: the sweep ran with flux-plunger possibly still running"
      false
    fi
    [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
    rm -rf "$tmp"
  done
}

@test "a flux-plunger Deployment that is already gone has nothing to stop, and says so" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  touch "$STATE/deploy.absent"
  echo 9999 > "$STATE/scale.fail"
  add_release "$STATE" tenant-a app-a "$FENCE"

  run_hook "$tmp"

  [ "$status" -eq 0 ]
  grep -q 'no flux-plunger Deployment in cozy-fluxcd; nothing to stop' "$tmp/out"
  [ "$(fences_left "$STATE")" -eq 0 ]
  if grep -q ' scale ' "$KLOG"; then
    echo "FAIL: scaled a Deployment that is not there"
    false
  fi
  rm -rf "$tmp"
}

@test "a flux-plunger pod that does not go is named, and the sweep goes on" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  # A pod stuck Terminating on a NotReady node stays until the node returns or
  # someone force-deletes it; failing on it would fail every retried uninstall.
  touch "$STATE/pods.stuck"
  add_release "$STATE" tenant-a app-a "$FENCE"
  echo 2 > "$STATE/tenant-a@app-a.patch.fail"

  run_hook "$tmp"

  [ "$status" -eq 0 ]
  grep -q 'pods are still running, sweeping anyway: pod/flux-plunger-0' "$tmp/out"
  [ "$(fences_left "$STATE")" -eq 0 ]
  grep -q -x 'released 1 of 1 fenced HelmReleases, 0 without an uninstall' "$tmp/out"
  [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
  rm -rf "$tmp"
}

@test "a release the still-running flux-plunger fences after the listing fails the hook and is named" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  # The stuck pod's container is alive: it never got SIGTERM, still holds the
  # Lease, and fences one more release once the listing has returned. With the
  # chart gone nothing would ever remove that fence.
  touch "$STATE/pods.stuck"
  add_release "$STATE" tenant-a app-a "$FENCE"
  add_release "$STATE" tenant-z app-z example.com/keep
  touch "$STATE/tenant-z@app-z.latefence"

  run_hook "$tmp"

  [ "$status" -ne 0 ]
  grep -q '^NOT released tenant-z/app-z: fenced after the sweep' "$tmp/out"
  [ "$(fences_left "$STATE")" -eq 1 ]
  rm -rf "$tmp"
}

@test "a pod wait that fails for another reason is told apart from a pod that stays" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  echo 1 > "$STATE/wait.fail"
  add_release "$STATE" tenant-a app-a "$FENCE"

  run_hook "$tmp"

  [ "$status" -eq 0 ]
  grep -q 'the wait for flux-plunger pods to go failed, but none is left' "$tmp/out"
  [ "$(fences_left "$STATE")" -eq 0 ]
  # With the pods gone, nothing can have fenced behind the sweep.
  [ "$(grep --count 'helmreleases.helm.toolkit.fluxcd.io --all-namespaces' "$KLOG")" -eq 1 ]
  rm -rf "$tmp"
}

@test "a pod wait after a slow scale-down is cut to what is left of the retry window" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  # The scale-down is retried for most of the window, and the pod never goes:
  # the wait must not run past the window, or the Job is killed before the
  # sweep and its report.
  echo 80 > "$STATE/scale.fail"
  touch "$STATE/pods.stuck"
  add_release "$STATE" tenant-a app-a "$FENCE"

  run_hook "$tmp"

  [ "$status" -eq 0 ]
  grep -q -x 'released 1 of 1 fenced HelmReleases, 0 without an uninstall' "$tmp/out"
  [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
  rm -rf "$tmp"
}

@test "a pod listing that cannot be read after a failed wait is treated as a pod still running" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  echo 1 > "$STATE/wait.fail"
  echo 9999 > "$STATE/pods.fail"
  add_release "$STATE" tenant-a app-a "$FENCE"
  add_release "$STATE" tenant-z app-z example.com/keep
  touch "$STATE/tenant-z@app-z.latefence"

  run_hook "$tmp"

  [ "$status" -ne 0 ]
  grep -q 'could not tell whether flux-plunger pods are still running; sweeping anyway' "$tmp/out"
  grep -q '^NOT released tenant-z/app-z: fenced after the sweep' "$tmp/out"
  [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
  rm -rf "$tmp"
}

@test "a listing after the sweep that never succeeds fails the hook" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  # The pod is still there, so whether it fenced anything behind the sweep is
  # unknown until a listing answers.
  touch "$STATE/pods.stuck"
  echo 9999 > "$STATE/relist.fail"
  add_release "$STATE" tenant-a app-a "$FENCE"

  run_hook "$tmp"

  [ "$status" -ne 0 ]
  grep -q 'could not list fenced HelmReleases again after the sweep' "$tmp/out"
  [ "$(fences_left "$STATE")" -eq 0 ]
  [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
  rm -rf "$tmp"
}

@test "a scale-down that returns after the retry window still bounds the pod wait" {
  tmp=$(mktemp -d)
  prepare "$tmp"
  # What is left of the window is then negative, which kubectl wait reads as a
  # week: the Job would be killed before the sweep and its report.
  echo $((WINDOW + 5)) > "$STATE/scale.slow"
  touch "$STATE/pods.stuck"
  add_release "$STATE" tenant-a app-a "$FENCE"

  run_hook "$tmp"

  [ "$status" -eq 0 ]
  grep -q -x 'released 1 of 1 fenced HelmReleases, 0 without an uninstall' "$tmp/out"
  [ "$(cat "$CLOCK")" -lt "$DEADLINE" ]
  rm -rf "$tmp"
}
