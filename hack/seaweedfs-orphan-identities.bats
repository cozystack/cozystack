#!/usr/bin/env bats
# Unit tests for packages/system/seaweedfs/files/orphan-identities.sh, the
# post-upgrade hook that removes the per-identity IAM files of BucketAccesses
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
# A weed shell session over $IDS, one file per identity holding its mtime.
# Like the real one it reports a failed command on stderr and exits 0.
# WEED_SLEEP hangs every session. WEED_LS_FAIL fails a listing (WEED_LS_FAIL_FROM: from that listing on),
# WEED_META_FAIL names a file whose metadata cannot be read, WEED_RM_FAIL makes
# every removal fail.
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
    fs.meta.cat)
      name=${path##*/}
      if [ "$name" = "${WEED_META_FAIL:-}" ] || [ ! -f "$IDS/$name" ]; then
        echo "error: $path: no entry is found in filer store" >&2
        continue
      fi
      # protojson renders int64 as a string and ends without a newline, so
      # the trailer lands on the closing brace's line.
      printf '{\n  "name": "%s",\n  "attributes": {\n    "mtime": "%s",\n    "crtime": "1"\n  },\n  "content": "U0VDUkVULWtleQ=="\n}chunks 0 meta size: 120 gzip:110\n' \
        "$name" "$(cat "$IDS/$name")"
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
case $1 in
  get)
    case $2 in
      helmrelease) printf 0b5e1a7e-0000-4000-8000-000000000001 ;;
      *)
        [ -z "${KUBECTL_BA_FAIL:-}" ] || { echo 'Error from server (Forbidden): bucketaccesses is forbidden' >&2; exit 1; }
        # Child tenants keep their BucketAccesses in their own namespaces.
        case " $* " in
          *" --all-namespaces "*) cat "$BA_JSON" ;;
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
  export MODE=delete MIN_AGE=600 SHELL_TIMEOUT=5
  NOW=$(date +%s)
}

# identity <file name> <age in seconds>
identity() {
  echo $((NOW - $2)) > "$IDS/$1"
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
  identity "ba-$GONE.json" 3600
  identity "ba-$LIVE.json" 3600
  bucketaccess "$LIVE" "ba-$LIVE"
  "$SCRIPT"
  removed "ba-$GONE.json"
  kept "ba-$LIVE.json"
}

@test "a BucketAccess still being granted protects its file by uid alone" {
  identity "ba-$LIVE.json" 3600
  bucketaccess "$LIVE"
  "$SCRIPT"
  kept "ba-$LIVE.json"
}

@test "a BucketAccess restored under a new uid protects the file its accountID names" {
  identity "ba-$GONE.json" 3600
  bucketaccess "$LIVE" "ba-$GONE"
  "$SCRIPT"
  kept "ba-$GONE.json"
}

@test "an empty BucketAccess listing removes nothing" {
  identity "ba-$GONE.json" 3600
  echo '{"kind":"List","items":[]}' > "$BA_JSON"
  "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^reason: OrphanIdentitiesFound$' "$KUBECTL_LOG"
}

@test "all ages are read in one weed shell session" {
  identity "ba-$GONE.json" 3600
  identity "ba-$GONE2.json" 3600
  "$SCRIPT"
  # listing, ages, removals, listing again
  [ "$(grep -c '^session' "$WEED_LOG")" -eq 4 ]
}

@test "a file younger than MIN_AGE is kept, one older is removed" {
  identity "ba-$GONE.json" 590
  identity "ba-$GONE2.json" 610
  "$SCRIPT"
  kept "ba-$GONE.json"
  removed "ba-$GONE2.json"
}

@test "identities the COSI driver did not name are never touched" {
  identity admin.json 3600
  identity ba-backup.json 3600
  identity "ba-$GONE.json.old" 3600
  identity "BA-$GONE.json" 3600
  identity "xba-$GONE.json" 3600
  identity "ba-${GONE}0.json" 3600
  identity ba-1234.json 3600
  "$SCRIPT"
  kept "xba-$GONE.json"
  kept "ba-${GONE}0.json"
  kept ba-1234.json
  if grep -q '^fs.meta.cat' "$WEED_LOG"; then echo "FAIL: read the age of a file the driver did not name"; cat "$WEED_LOG"; false; fi
  kept admin.json
  kept ba-backup.json
  kept "ba-$GONE.json.old"
  kept "BA-$GONE.json"
  if grep -q '^fs.rm' "$WEED_LOG"; then echo "FAIL: removed something"; cat "$WEED_LOG"; false; fi
}

@test "report mode names the orphan and removes nothing" {
  identity "ba-$GONE.json" 3600
  out=$(MODE=report "$SCRIPT" 2>&1)
  kept "ba-$GONE.json"
  if grep -q '^fs.rm' "$WEED_LOG"; then echo "FAIL: report mode removed a file"; false; fi
  case $out in *"ba-$GONE"*) ;; *) echo "FAIL: orphan not named: $out"; false ;; esac
  grep -q '^reason: OrphanIdentitiesFound$' "$KUBECTL_LOG"
  grep -q "ba-$GONE" "$KUBECTL_LOG"
}

@test "an unknown mode only reports" {
  identity "ba-$GONE.json" 3600
  MODE=Delete "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesFound$' "$KUBECTL_LOG"
}

@test "a BucketAccess listing that fails removes nothing and ends in a Warning Event" {
  identity "ba-$GONE.json" 3600
  KUBECTL_BA_FAIL=1 "$SCRIPT"
  kept "ba-$GONE.json"
  if grep -q '^fs' "$WEED_LOG"; then echo "FAIL: touched the filer without a BucketAccess listing"; false; fi
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a BucketAccess listing that is not a list removes nothing" {
  identity "ba-$GONE.json" 3600
  echo '{"kind":"Status","status":"Failure"}' > "$BA_JSON"
  "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a listing item without a uid removes nothing" {
  identity "ba-$GONE.json" 3600
  echo '{"kind":"List","items":[{"metadata":{}}]}' > "$BA_JSON"
  "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a listing of the identity directory that exits non-zero removes nothing and ends in a Warning Event" {
  identity "ba-$GONE.json" 3600
  WEED_LS_FAIL="exit" "$SCRIPT"
  kept "ba-$GONE.json"
  if grep -q '^fs.rm' "$WEED_LOG"; then echo "FAIL: removed a file"; false; fi
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a listing of the identity directory that reports an error removes nothing" {
  identity "ba-$GONE.json" 3600
  WEED_LS_FAIL="print" "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "a file whose age cannot be read is kept, the others are still handled" {
  identity "ba-$GONE.json" 3600
  identity "ba-$GONE2.json" 3600
  WEED_META_FAIL="ba-$GONE.json" "$SCRIPT"
  kept "ba-$GONE.json"
  removed "ba-$GONE2.json"
}

@test "every removal is logged and recorded as an Event on the HelmRelease" {
  identity "ba-$GONE.json" 3600
  identity "ba-$GONE2.json" 3600
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
  identity "ba-$GONE.json" 3600
  WEED_LS_FAIL="exit" WEED_LS_FAIL_FROM=2 "$SCRIPT"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^reason: OrphanIdentityNotRemoved$' "$KUBECTL_LOG"
  if grep -q '^reason: OrphanIdentityRemoved$' "$KUBECTL_LOG"; then echo "FAIL: claimed a removal it could not confirm"; false; fi
}

@test "a listing after the removal that reports an error claims no removal" {
  identity "ba-$GONE.json" 3600
  WEED_LS_FAIL="print" WEED_LS_FAIL_FROM=2 "$SCRIPT"
  grep -q '^reason: OrphanIdentityNotRemoved$' "$KUBECTL_LOG"
  if grep -q '^reason: OrphanIdentityRemoved$' "$KUBECTL_LOG"; then echo "FAIL: claimed a removal it could not confirm"; false; fi
}

@test "a removal that fails ends in a Warning Event and exit 0" {
  identity "ba-$GONE.json" 3600
  WEED_RM_FAIL=1 "$SCRIPT"
  kept "ba-$GONE.json"
  grep -q '^type: Warning$' "$KUBECTL_LOG"
  grep -q '^reason: OrphanIdentityNotRemoved$' "$KUBECTL_LOG"
}

@test "nothing to do records no Event" {
  identity "ba-$LIVE.json" 3600
  bucketaccess "$LIVE"
  "$SCRIPT"
  if grep -q '^ARGS: create' "$KUBECTL_LOG"; then echo "FAIL: recorded an Event"; false; fi
}

@test "a weed shell that never returns is cut off and removes nothing" {
  identity "ba-$GONE.json" 3600
  start=$(date +%s)
  SHELL_TIMEOUT=1 WEED_SLEEP=30 "$SCRIPT"
  [ $(($(date +%s) - start)) -lt 10 ]
  kept "ba-$GONE.json"
  grep -q '^reason: OrphanIdentitiesNotChecked$' "$KUBECTL_LOG"
}

@test "secret content of an identity file never reaches the output" {
  identity "ba-$GONE.json" 3600
  out=$("$SCRIPT" 2>&1)
  case $out in *U0VDUkVU*) echo "FAIL: file content printed: $out"; false ;; esac
  if grep -q U0VDUkVU "$KUBECTL_LOG"; then echo "FAIL: file content in an Event"; false; fi
}
