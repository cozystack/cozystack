#!/usr/bin/env bats
# Unit tests for packages/system/seaweedfs/files/object-lock-actions.sh, the
# post-upgrade hook that grants the dedicated Object Lock actions SeaweedFS 4.48
# checks in place of Read and Write. Drives it against a fake `weed shell` that
# keeps the IAM configuration in a JSON file, and a fake kubectl.

load test_helper

SCRIPT="$PWD/packages/system/seaweedfs/files/object-lock-actions.sh"

setup() {
  strict_setup
  BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$BIN"
  export STATE="$BATS_TEST_TMPDIR/iam.json"
  export CALLS="$BATS_TEST_TMPDIR/calls"
  export KUBECTL_LOG="$BATS_TEST_TMPDIR/kubectl"
  : > "$CALLS"
  : > "$KUBECTL_LOG"
  cat > "$BIN/weed" <<'EOF'
#!/bin/bash
# A weed shell session over $STATE. WEED_SLEEP hangs it, WEED_NOOP_APPLY drops
# every apply, WEED_DROP_SESSIONS drops the applies of that many sessions,
# WEED_READS_OK lets only that many listings succeed, and
# WEED_UNRESOLVED names a user that GetUser cannot find, the
# way s3.configure sees one: a fresh identity with no keys, which the apply
# then stores over the listed one.
set -eu
[ "$1" = shell ] || exit 2
[ -n "${WEED_SLEEP:-}" ] && sleep "$WEED_SLEEP"
drop=false
if [ -n "${WEED_DROP_SESSIONS:-}" ]; then
  n=$(cat "$STATE.sessions" 2>/dev/null || echo 0)
  # Only sessions that apply count.
  input=$(cat)
  case $input in *" -apply"*)
    n=$((n + 1)); echo "$n" > "$STATE.sessions"
    [ "$n" -le "$WEED_DROP_SESSIONS" ] && drop=true ;;
  esac
  exec <<< "$input"
fi
while read -r line; do
  case $line in
    "s3.configure")
      if [ -n "${WEED_READS_OK:-}" ]; then
        reads=$(cat "$STATE.reads" 2>/dev/null || echo 0)
        echo $((reads + 1)) > "$STATE.reads"
        [ "$reads" -lt "$WEED_READS_OK" ] || exit 1
      fi
      cat "$STATE"
      ;;
    "s3.configure -user="*)
      echo "$line" >> "$CALLS"
      user=${line#s3.configure -user=}; user=${user%% *}
      rest=${line#* -actions=}
      actions=${rest%% *}
      apply=false
      case $rest in *" -apply") apply=true ;; esac
      if [ "$user" = "${WEED_UNRESOLVED:-}" ]; then
        current='{"name":"'"$user"'","credentials":[],"actions":[]}'
      else
        current=$(jq -c --arg u "$user" '.identities[] | select(.name == $u)' "$STATE")
      fi
      updated=$(jq -c --arg a "$actions" '.actions = reduce ($a | split(","))[] as $x (.actions; if index([$x]) then . else . + [$x] end)' <<< "$current")
      if $apply; then
        { [ -n "${WEED_NOOP_APPLY:-}" ] || $drop; } && continue
        jq --arg u "$user" --argjson id "$updated" \
          '.identities |= map(if .name == $u then $id else . end)' "$STATE" > "$STATE.new"
        mv "$STATE.new" "$STATE"
        echo "$updated"
      else
        echo "$updated"
        echo 'Running in simulation mode. Use "-apply" option to apply the changes.'
      fi
      ;;
    *)
      exit 3
      ;;
  esac
done
EOF
  cat > "$BIN/kubectl" <<'EOF'
#!/bin/sh
case $1 in
  get) echo "ARGS: $*" >> "$KUBECTL_LOG"; printf 0b5e1a7e-0000-4000-8000-000000000001 ;;
  *) { echo "ARGS: $*"; cat; } >> "$KUBECTL_LOG" ;;
esac
EOF
  chmod +x "$BIN/weed" "$BIN/kubectl"
  export PATH="$BIN:$PATH"
  export MASTER=seaweedfs-master:9333 FILER=seaweedfs-filer-client:8888
  export NAMESPACE=tenant-foo RELEASE=seaweedfs-system
  export ATTEMPTS=2 RETRY_DELAY=0 SHELL_TIMEOUT=5
}

identity() {
  # identity <name> <actions-json-array> [isStatic]
  printf '{"name":"%s","credentials":[{"accessKey":"AK-%s","secretKey":"SECRET-%s"}],"actions":%s,"isStatic":%s}' \
    "$1" "$1" "$1" "$2" "${3:-false}"
}

state() {
  printf '{"identities":[%s]}\n' "$(IFS=,; echo "$*")" > "$STATE"
}

actions_of() {
  jq -c --arg u "$1" '.identities[] | select(.name == $u) | .actions | sort' "$STATE"
}

@test "readwrite identity gains the Get and Put Object Lock actions for its bucket" {
  state "$(identity rw '["Read:b1","Write:b1","List:b1","Tagging:b1"]')"
  "$SCRIPT"
  [ "$(actions_of rw)" = '["GetBucketObjectLockConfiguration:b1","GetObjectLegalHold:b1","GetObjectRetention:b1","List:b1","PutBucketObjectLockConfiguration:b1","PutObjectLegalHold:b1","PutObjectRetention:b1","Read:b1","Tagging:b1","Write:b1"]' ]
}

@test "readonly identity gains only the Get Object Lock actions" {
  state "$(identity ro '["Read:b1","List:b1"]')"
  "$SCRIPT"
  [ "$(actions_of ro)" = '["GetBucketObjectLockConfiguration:b1","GetObjectLegalHold:b1","GetObjectRetention:b1","List:b1","Read:b1"]' ]
}

@test "the suffix of each grant is carried over verbatim, bare grants stay bare" {
  state "$(identity w '["Write","Read:logs/*"]')"
  "$SCRIPT"
  [ "$(actions_of w)" = '["GetBucketObjectLockConfiguration:logs/*","GetObjectLegalHold:logs/*","GetObjectRetention:logs/*","PutBucketObjectLockConfiguration","PutObjectLegalHold","PutObjectRetention","Read:logs/*","Write"]' ]
}

@test "actions that only start with Read or Write imply nothing" {
  state "$(identity acp '["ReadAcp:b1","WriteAcp:b1","List:b1"]')"
  "$SCRIPT"
  [ "$(actions_of acp)" = '["List:b1","ReadAcp:b1","WriteAcp:b1"]' ]
  [ ! -s "$CALLS" ]
}

@test "admin and static identities are left alone" {
  state "$(identity admin '["Admin"]')" "$(identity static '["Read","Write"]' true)"
  "$SCRIPT"
  [ "$(actions_of admin)" = '["Admin"]' ]
  [ "$(actions_of static)" = '["Read","Write"]' ]
  [ ! -s "$CALLS" ]
}

@test "a second run changes nothing" {
  state "$(identity rw '["Read:b1","Write:b1"]')" "$(identity ro '["Read:b2"]')"
  "$SCRIPT"
  [ "$(grep -c -- ' -apply$' "$CALLS")" -eq 2 ]
  : > "$CALLS"
  "$SCRIPT"
  [ ! -s "$CALLS" ]
}

@test "secret keys never reach the output" {
  state "$(identity rw '["Read:b1","Write:b1"]')"
  out=$("$SCRIPT" 2>&1)
  case $out in
    *SECRET-*) echo "FAIL: secret key printed: $out"; false ;;
  esac
  case $out in
    *rw*) ;;
    *) echo "FAIL: the identity it changed is not named: $out"; false ;;
  esac
}

@test "a grant that does not stick ends in a Warning Event on the HelmRelease and exit 0" {
  state "$(identity rw '["Read:b1","Write:b1"]')"
  WEED_NOOP_APPLY=1 "$SCRIPT"
  [ "$(grep -c -- ' -apply$' "$CALLS")" -eq 2 ]
  grep -q '^ARGS: create --filename -$' "$KUBECTL_LOG"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^  kind: HelmRelease$' "$KUBECTL_LOG"
  grep -q '^  name: seaweedfs-system$' "$KUBECTL_LOG"
  grep -q '^  namespace: tenant-foo$' "$KUBECTL_LOG"
  grep -q '^  uid: "0b5e1a7e-0000-4000-8000-000000000001"$' "$KUBECTL_LOG"
  grep -q 'rw' "$KUBECTL_LOG"
}

@test "an unreachable filer ends in a Warning Event and exit 0" {
  rm -f "$STATE"
  "$SCRIPT"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q 'could not be read' "$KUBECTL_LOG"
}

@test "a weed shell that never returns is cut off and still ends in an Event" {
  state "$(identity rw '["Read:b1","Write:b1"]')"
  SHELL_TIMEOUT=1 WEED_SLEEP=30 "$SCRIPT"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
}

@test "an identity s3.configure cannot resolve is never applied over" {
  state "$(identity rw '["Read:b1","Write:b1"]')" "$(identity lost '["Read:b2","Write:b2"]')"
  WEED_UNRESOLVED=lost "$SCRIPT"
  [ "$(jq -c '.identities[] | select(.name == "lost")' "$STATE")" = '{"name":"lost","credentials":[{"accessKey":"AK-lost","secretKey":"SECRET-lost"}],"actions":["Read:b2","Write:b2"],"isStatic":false}' ]
  if grep -q -- '-user=lost .* -apply$' "$CALLS"; then echo "FAIL: applied over an unresolved identity"; false; fi
  [ "$(actions_of rw)" = '["GetBucketObjectLockConfiguration:b1","GetObjectLegalHold:b1","GetObjectRetention:b1","PutBucketObjectLockConfiguration:b1","PutObjectLegalHold:b1","PutObjectRetention:b1","Read:b1","Write:b1"]' ]
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q 'lost' "$KUBECTL_LOG"
}

@test "output without an IAM configuration counts as a failed read" {
  echo 'error: connection refused' > "$STATE"
  "$SCRIPT"
  grep -q 'could not be read' "$KUBECTL_LOG"
}

@test "no Event is written when every grant sticks" {
  state "$(identity rw '["Read:b1","Write:b1"]')"
  "$SCRIPT"
  [ ! -s "$KUBECTL_LOG" ]
}

@test "JSON that is not an IAM configuration counts as a failed read" {
  echo '{"error":"not ready"}' > "$STATE"
  "$SCRIPT"
  grep -q 'could not be read' "$KUBECTL_LOG"
}

@test "grants that stick only on the last attempt raise no Event" {
  state "$(identity rw '["Read:b1","Write:b1"]')"
  WEED_DROP_SESSIONS=1 "$SCRIPT"
  [ "$(grep -c -- ' -apply$' "$CALLS")" -eq 2 ]
  [ ! -s "$KUBECTL_LOG" ]
}

@test "a name or action weed shell would split never reaches a command line and is reported" {
  state "$(identity rw '["Read:b1","Write:b1"]')" "$(identity 'x;s3.configure -user=rw -actions=Admin -apply' '["Read:b2"]')" "$(identity y '["Read:b3;s3.configure -user=rw -actions=Admin"]')"
  "$SCRIPT"
  if grep -q -- 'x;' "$CALLS"; then echo "FAIL: unsafe name reached weed shell"; false; fi
  if grep -q -- 'Admin' "$CALLS"; then echo "FAIL: injected action reached weed shell"; false; fi
  [ "$(actions_of rw)" = '["GetBucketObjectLockConfiguration:b1","GetObjectLegalHold:b1","GetObjectRetention:b1","PutBucketObjectLockConfiguration:b1","PutObjectLegalHold:b1","PutObjectRetention:b1","Read:b1","Write:b1"]' ]
  grep -q '^type: Warning$' "$KUBECTL_LOG"
}

@test "a final read that fails reports the outcome as unverified, not the last plan" {
  state "$(identity rw '["Read:b1","Write:b1"]')"
  WEED_READS_OK=1 "$SCRIPT"
  [ "$(actions_of rw)" = '["GetBucketObjectLockConfiguration:b1","GetObjectLegalHold:b1","GetObjectRetention:b1","PutBucketObjectLockConfiguration:b1","PutObjectLegalHold:b1","PutObjectRetention:b1","Read:b1","Write:b1"]' ]
  grep -q 'could not be read' "$KUBECTL_LOG"
  if grep -q 'missing them' "$KUBECTL_LOG"; then echo "FAIL: reported a stale plan"; false; fi
}
