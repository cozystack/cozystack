#!/usr/bin/env bats
# Unit tests for packages/system/seaweedfs/files/orphan-identities.sh, the
# post-install/post-upgrade hook that removes the per-identity IAM files of BucketAccesses
# that no longer exist. Drives it against a fake `weed shell` that keeps the
# identity files in a directory, and a fake kubectl.

load test_helper

SCRIPT="$PWD/packages/system/seaweedfs/files/orphan-identities.sh"

OTHER=0b5e1a7e-0000-4000-8000-000000000009
LIVE=0b5e1a7e-0000-4000-8000-00000000000a
GONE=0b5e1a7e-0000-4000-8000-00000000000b
GONE2=0b5e1a7e-0000-4000-8000-00000000000c

setup() {
  strict_setup
  BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$BIN"
  export IDS="$BATS_TEST_TMPDIR/identities"
  export BA_JSON="$BATS_TEST_TMPDIR/bucketaccesses.json"
  export KUBECTL_LOG="$BATS_TEST_TMPDIR/kubectl"
  export WEED_LOG="$BATS_TEST_TMPDIR/weed"
  mkdir -p "$IDS"
  : > "$KUBECTL_LOG"
  : > "$WEED_LOG"
  # One BucketAccess with no file, so the listing is never empty by accident.
  echo '{"apiVersion":"v1","kind":"List","items":[{"metadata":{"uid":"'"$OTHER"'"},"status":{"accountID":"ba-'"$OTHER"'"}}]}' > "$BA_JSON"
  cat > "$BIN/weed" <<'EOF'
#!/bin/bash
# A weed shell session over $IDS, one file per identity.
# Like the real one it reports a failed command on stderr and exits 0.
# WEED_SLEEP hangs every session. WEED_LS_FAIL fails a listing (WEED_LS_FAIL_FROM: from that listing on),
# WEED_RM_FAIL makes every removal fail.
set -eu
[ "$1" = shell ] || exit 2
echo session >> "$WEED_LOG"
[ -z "${WEED_SLEEP:-}" ] || sleep "$WEED_SLEEP"
while read -r cmd path; do
  echo "$cmd $path" >> "$WEED_LOG"
  case $cmd in
    fs.ls)
      [ "$path" = /etc/iam/identities ] || exit 3
      n=$(($(cat "$WEED_LOG.ls" 2>/dev/null || echo 0) + 1)); echo "$n" > "$WEED_LOG.ls"
      [ "$n" -ge "${WEED_LS_FAIL_FROM:-1}" ] || { ls -1 "$IDS"; continue; }
      case ${WEED_LS_FAIL:-} in
        exit) exit 1 ;;
        print) echo "error: list $path: connection refused" >&2; continue ;;
      esac
      ls -1 "$IDS"
      ;;
    fs.rm)
      [ -z "${WEED_RM_FAIL:-}" ] || { echo "error: rm $path: permission denied" >&2; continue; }
      rm "$IDS/${path##*/}"
      ;;
    *)
      exit 4
      ;;
  esac
done
EOF
  cat > "$BIN/kubectl" <<'EOF'
#!/bin/sh
echo "ARGS: $*" >> "$KUBECTL_LOG"
# Like the real one: any --request-timeout drops the in-cluster credentials.
case " $* " in
  *" --request-timeout"*) echo 'The connection to the server localhost:8080 was refused' >&2; exit 1 ;;
esac
[ -z "${KUBECTL_SLEEP:-}" ] || exec sleep "$KUBECTL_SLEEP"
case $1 in
  get)
    case $2 in
      helmrelease) printf 0b5e1a7e-0000-4000-8000-000000000001 ;;
      *)
        [ -z "${KUBECTL_BA_FAIL:-}" ] || { echo 'Error from server (Forbidden): bucketaccesses is forbidden' >&2; exit 1; }
        # Child tenants keep their BucketAccesses in their own namespaces.
        case " $* " in
          *" --all-namespaces "*)
            cat "$BA_JSON"
            # A BucketAccess created and granted right after this listing.
            [ -z "${KUBECTL_GRANT_AFTER:-}" ] || echo SECRET > "$IDS/ba-$KUBECTL_GRANT_AFTER.json"
            ;;
          *) echo '{"kind":"List","items":[]}' ;;
        esac
        ;;
    esac
    ;;
  *) cat >> "$KUBECTL_LOG" ;;
esac
EOF
  chmod +x "$BIN/weed" "$BIN/kubectl"
  export PATH="$BIN:$PATH"
  export MASTER=seaweedfs-master:9333 FILER=seaweedfs-filer-client:8888
  export NAMESPACE=tenant-foo RELEASE=seaweedfs-system JOB=seaweedfs-system-orphan-identities
  export MODE=delete SHELL_TIMEOUT=5
}

# identity <file name>
identity() {
  echo SECRET > "$IDS/$1"
}

# bucketaccess <uid> [accountID]
bucketaccess() {
  jq --arg uid "$1" --arg acct "${2:-}" \
    '.items += [{"metadata": {"uid": $uid}, "status": (if $acct == "" then {} else {"accountID": $acct} end)}]' \
    "$BA_JSON" > "$BA_JSON.new"
  mv "$BA_JSON.new" "$BA_JSON"
}

kept() {
  [ -f "$IDS/$1" ] || { echo "FAIL: $1 was removed"; false; }
}

removed() {
  if [ -f "$IDS/$1" ]; then echo "FAIL: $1 is still there"; false; fi
}

@test "an old identity file with no BucketAccess behind it is removed" {
  identity "ba-$GONE.json"
  identity "ba-$LIVE.json"
  bucketaccess "$LIVE" "ba-$LIVE"
  "$SCRIPT"
  removed "ba-$GONE.json"
  kept "ba-$LIVE.json"
}

@test "a BucketAccess still being granted protects its file by uid alone" {
  identity "ba-$LIVE.json"
  bucketaccess "$LIVE"
  "$SCRIPT"
  kept "ba-$LIVE.json"
}

@test "a BucketAccess restored under a new uid protects the file its accountID names" {
  identity "ba-$GONE.json"
  bucketaccess "$LIVE" "ba-$GONE"
  "$SCRIPT"
  kept "ba-$GONE.json"
}

@test "an empty BucketAccess listing removes nothing" {
  identity "ba-$GONE.json"
  echo '{"kind":"List","items":[]}' > "$BA_JSON"
  "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^reason: OrphanIdentitiesFound$' "$KUBECTL_LOG"
}

@test "the identity directory is listed before the BucketAccesses" {
  KUBECTL_GRANT_AFTER=$LIVE "$SCRIPT"
  kept "ba-$LIVE.json"
}

@test "a file rewritten by a grant a moment ago is removed once its BucketAccess is gone" {
  identity "ba-$GONE.json"
  touch "$IDS/ba-$GONE.json"
  "$SCRIPT"
  removed "ba-$GONE.json"
}

@test "listing, removals and the check listing take one weed shell session each" {
  identity "ba-$GONE.json"
  identity "ba-$GONE2.json"
  "$SCRIPT"
  [ "$(grep -c '^session' "$WEED_LOG")" -eq 3 ]
}

@test "identities the COSI driver did not name are never touched" {
  identity admin.json
  identity ba-backup.json
  identity "ba-$GONE.json.old"
  identity "BA-$GONE.json"
  identity "xba-$GONE.json"
  identity "ba-${GONE}0.json"
  identity ba-1234.json
  "$SCRIPT"
  kept "xba-$GONE.json"
  kept "ba-${GONE}0.json"
  kept ba-1234.json
  kept admin.json
  kept ba-backup.json
  kept "ba-$GONE.json.old"
  kept "BA-$GONE.json"
  if grep -q '^fs.rm' "$WEED_LOG"; then echo "FAIL: removed something"; cat "$WEED_LOG"; false; fi
}

@test "report mode names the orphan and removes nothing" {
  identity "ba-$GONE.json"
  out=$(MODE=report "$SCRIPT" 2>&1)
  kept "ba-$GONE.json"
  if grep -q '^fs.rm' "$WEED_LOG"; then echo "FAIL: report mode removed a file"; false; fi
  case $out in *"ba-$GONE"*) ;; *) echo "FAIL: orphan not named: $out"; false ;; esac
  grep -q '^reason: OrphanIdentitiesFound$' "$KUBECTL_LOG"
  grep -q "ba-$GONE" "$KUBECTL_LOG"
  grep -q 'not removed (mode report)' "$KUBECTL_LOG"
  grep -q 'cosi.orphanIdentities to delete' "$KUBECTL_LOG"
  if grep -q 'other clusters' "$KUBECTL_LOG"; then echo "FAIL: Event claims a reason the script cannot know"; false; fi
}

@test "an unknown mode only reports" {
  identity "ba-$GONE.json"
  MODE=Delete "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesFound$' "$KUBECTL_LOG"
  grep -q 'not removed (mode Delete)' "$KUBECTL_LOG"
}

@test "a BucketAccess listing that fails removes nothing and ends in a Warning Event" {
  identity "ba-$GONE.json"
  KUBECTL_BA_FAIL=1 "$SCRIPT"
  kept "ba-$GONE.json"
  if grep -q '^fs.rm' "$WEED_LOG"; then echo "FAIL: removed a file without a BucketAccess listing"; false; fi
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a BucketAccess listing that is not a list removes nothing" {
  identity "ba-$GONE.json"
  echo '{"kind":"Status","status":"Failure"}' > "$BA_JSON"
  "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a listing item without a uid removes nothing" {
  identity "ba-$GONE.json"
  echo '{"kind":"List","items":[{"metadata":{}}]}' > "$BA_JSON"
  "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a listing of the identity directory that exits non-zero removes nothing and ends in a Warning Event" {
  identity "ba-$GONE.json"
  WEED_LS_FAIL="exit" "$SCRIPT"
  kept "ba-$GONE.json"
  if grep -q '^fs.rm' "$WEED_LOG"; then echo "FAIL: removed a file"; false; fi
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a listing of the identity directory that reports an error removes nothing" {
  identity "ba-$GONE.json"
  WEED_LS_FAIL="print" "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "every removal is logged and recorded as an Event on the HelmRelease" {
  identity "ba-$GONE.json"
  identity "ba-$GONE2.json"
  out=$("$SCRIPT" 2>&1)
  case $out in *"removed ba-$GONE"*) ;; *) echo "FAIL: removal not logged: $out"; false ;; esac
  case $out in *"removed ba-$GONE2"*) ;; *) echo "FAIL: removal not logged: $out"; false ;; esac
  [ "$(grep -c '^reason: OrphanIdentityRemoved$' "$KUBECTL_LOG")" -eq 2 ]
  grep -q '^  kind: HelmRelease$' "$KUBECTL_LOG"
  grep -q '^  name: seaweedfs-system$' "$KUBECTL_LOG"
  grep -q '^  namespace: tenant-foo$' "$KUBECTL_LOG"
  grep -q '^  uid: "0b5e1a7e-0000-4000-8000-000000000001"$' "$KUBECTL_LOG"
}

@test "a listing that fails after the removal ends in a Warning Event" {
  identity "ba-$GONE.json"
  WEED_LS_FAIL="exit" WEED_LS_FAIL_FROM=2 "$SCRIPT"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^reason: OrphanIdentityNotRemoved$' "$KUBECTL_LOG"
  if grep -q '^reason: OrphanIdentityRemoved$' "$KUBECTL_LOG"; then echo "FAIL: claimed a removal it could not confirm"; false; fi
}

@test "a listing after the removal that reports an error claims no removal" {
  identity "ba-$GONE.json"
  WEED_LS_FAIL="print" WEED_LS_FAIL_FROM=2 "$SCRIPT"
  grep -q '^reason: OrphanIdentityNotRemoved$' "$KUBECTL_LOG"
  if grep -q '^reason: OrphanIdentityRemoved$' "$KUBECTL_LOG"; then echo "FAIL: claimed a removal it could not confirm"; false; fi
}

@test "a removal that fails ends in a Warning Event and exit 0" {
  identity "ba-$GONE.json"
  WEED_RM_FAIL=1 "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^reason: OrphanIdentityNotRemoved$' "$KUBECTL_LOG"
}

@test "nothing to do records no Event" {
  identity "ba-$LIVE.json"
  bucketaccess "$LIVE"
  "$SCRIPT"
  if grep -q '^ARGS: create' "$KUBECTL_LOG"; then echo "FAIL: recorded an Event"; false; fi
}

@test "a weed shell that never returns is cut off and removes nothing" {
  identity "ba-$GONE.json"
  start=$(date +%s)
  SHELL_TIMEOUT=1 WEED_SLEEP=30 "$SCRIPT"
  [ $(($(date +%s) - start)) -lt 10 ]
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "secret content of an identity file never reaches the output" {
  identity "ba-$GONE.json"
  out=$("$SCRIPT" 2>&1)
  case $out in *SECRET*) echo "FAIL: file content printed: $out"; false ;; esac
  if grep -q SECRET "$KUBECTL_LOG"; then echo "FAIL: file content in an Event"; false; fi
}

# The Object Lock hook grants every identity with Read or Write, left-over
# ones included; removal has to come first.
@test "orphan removal runs before the Object Lock grant" {
  helm template seaweedfs-system packages/system/seaweedfs --namespace tenant-foo > "$BATS_TEST_TMPDIR/manifest"
  weight() {
    yq eval-all "select(.kind == \"$1\" and .metadata.name == \"$2\") | .metadata.annotations[\"helm.sh/hook-weight\"]" "$BATS_TEST_TMPDIR/manifest"
  }
  orphan=$(weight Job seaweedfs-system-orphan-identities)
  grant=$(weight Job seaweedfs-system-object-lock-actions)
  [ -n "$orphan" ] && [ -n "$grant" ]
  [ "$orphan" -lt "$grant" ]
  for kind in ConfigMap ServiceAccount Role RoleBinding; do
    [ "$(weight "$kind" seaweedfs-system-orphan-identities)" -lt "$orphan" ]
  done
}

@test "a kubectl that never returns is cut off and removes nothing" {
  identity "ba-$GONE.json"
  start=$SECONDS
  SHELL_TIMEOUT=1 KUBECTL_SLEEP=30 "$SCRIPT"
  [ $((SECONDS - start)) -lt 10 ]
  kept "ba-$GONE.json"
  grep -q '^ARGS: get bucketaccesses' "$KUBECTL_LOG"
  grep -q '^ARGS: get helmrelease' "$KUBECTL_LOG"
  grep -q '^ARGS: create' "$KUBECTL_LOG"
}
