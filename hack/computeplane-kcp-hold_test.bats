#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/e2e-chainsaw/computeplane/kcp-hold.sh, the finalizer the
# computeplane teardown holds the KamajiControlPlane with. A release that
# reports success without removing the entry leaves the control plane and its
# tenant stuck in deletion for good, so every path that cannot prove the entry
# is gone has to fail.
#
# kubectl is a stub on PATH: `get` prints $FIX/kcp.json when it exists (an
# empty answer is --ignore-not-found on an absent object) and exits with
# $FIX/get-rc, and `patch` records its JSON patch in $FIX/patch and exits with
# $FIX/patch-rc.
#
# cozytest.sh runs the bodies under sh and puts `return 0` in front of every
# bare `}`, so the helpers whose status matters close with `} # name`.
#
# Run with: hack/cozytest.sh hack/computeplane-kcp-hold_test.bats
# -----------------------------------------------------------------------------

load test_helper

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
KH_LIB="$HACK_DIR/e2e-chainsaw/computeplane/kcp-hold.sh"
export KH_LIB

HOLD=e2e.cozystack.io/teardown-order

kh() {
  PATH="$FIX/bin:$PATH" sh -c '. "$KH_LIB"; "$@"' kh "$@"
} # kh

new_fixture() {
  FIX=$(mktemp -d)
  export FIX
  mkdir -p "$FIX/bin"
  cat >"$FIX/bin/kubectl" <<'EOF'
#!/bin/sh
case " $* " in
  *" get "*)
    [ ! -f "$FIX/kcp.json" ] || cat "$FIX/kcp.json"
    exit "$(cat "$FIX/get-rc" 2>/dev/null || echo 0)" ;;
  *" patch "*)
    for a; do last=$a; done
    printf '%s' "$last" >"$FIX/patch"
    exit "$(cat "$FIX/patch-rc" 2>/dev/null || echo 0)" ;;
esac
echo "kubectl stub: unexpected call: $*" >&2
exit 99
EOF
  chmod +x "$FIX/bin/kubectl"
} # new_fixture

# kcp_with FINALIZERS_JSON — the object the stub returns; "null" for none.
kcp_with() {
  printf '{"metadata":{"name":"computeplane-cluster","finalizers":%s}}' "$1" | jq 'del(.metadata.finalizers | select(. == null))' >"$FIX/kcp.json"
} # kcp_with

patch_is() {
  [ -f "$FIX/patch" ] || { echo "no patch was sent" >&2; return 1; }
  got=$(jq -cS . "$FIX/patch")
  want=$(printf '%s' "$1" | jq -cS .)
  [ "$got" = "$want" ] || { echo "patch: got $got, want $want" >&2; return 1; }
} # patch_is

@test "hold creates the finalizer list when the object has none" {
  new_fixture
  kcp_with null
  kh kcp_hold
  patch_is "[{\"op\":\"add\",\"path\":\"/metadata/finalizers\",\"value\":[\"$HOLD\"]}]"
  rm -rf "$FIX"
}

@test "hold appends behind a test of the existing list" {
  new_fixture
  kcp_with '["a.example/x"]'
  kh kcp_hold
  patch_is "[{\"op\":\"test\",\"path\":\"/metadata/finalizers\",\"value\":[\"a.example/x\"]},{\"op\":\"add\",\"path\":\"/metadata/finalizers/-\",\"value\":\"$HOLD\"}]"
  rm -rf "$FIX"
}

@test "hold returns 2 and patches nothing when the object is absent" {
  new_fixture
  rc=0
  kh kcp_hold || rc=$?
  [ "$rc" -eq 2 ] || { echo "rc=$rc, want 2" >&2; exit 1; }
  [ ! -f "$FIX/patch" ] || { echo "patched an absent object" >&2; exit 1; }
  rm -rf "$FIX"
}

@test "hold fails when the patch fails" {
  new_fixture
  kcp_with null
  echo 1 >"$FIX/patch-rc"
  if kh kcp_hold; then echo "hold succeeded on a failed patch" >&2; exit 1; fi
  rm -rf "$FIX"
}

@test "hold fails without patching when the object cannot be read as JSON" {
  new_fixture
  echo 'not json' >"$FIX/kcp.json"
  if kh kcp_hold; then echo "hold succeeded on unreadable input" >&2; exit 1; fi
  [ ! -f "$FIX/patch" ] || { echo "patched with $(cat "$FIX/patch")" >&2; exit 1; }
  rm -rf "$FIX"
}

@test "hold fails with 1, not the absent object's 2, when the read fails" {
  new_fixture
  echo 1 >"$FIX/get-rc"
  rc=0
  kh kcp_hold || rc=$?
  [ "$rc" -eq 1 ] || { echo "rc=$rc, want 1" >&2; exit 1; }
  [ ! -f "$FIX/patch" ] || { echo "patched after a failed read" >&2; exit 1; }
  rm -rf "$FIX"
}

@test "release removes its own entry by index behind a test of that index" {
  new_fixture
  kcp_with "[\"a.example/x\",\"$HOLD\",\"b.example/y\"]"
  out=$(kh kcp_release)
  patch_is "[{\"op\":\"test\",\"path\":\"/metadata/finalizers/1\",\"value\":\"$HOLD\"},{\"op\":\"remove\",\"path\":\"/metadata/finalizers/1\"}]"
  [ -n "$out" ] || { echo "release printed nothing" >&2; exit 1; }
  rm -rf "$FIX"
}

@test "release succeeds without patching when the entry is not there" {
  new_fixture
  kcp_with '["a.example/x"]'
  kh kcp_release
  [ ! -f "$FIX/patch" ] || { echo "patched with $(cat "$FIX/patch")" >&2; exit 1; }
  rm -rf "$FIX"
}

@test "release succeeds without patching when the object is gone" {
  new_fixture
  kh kcp_release
  [ ! -f "$FIX/patch" ] || { echo "patched an absent object" >&2; exit 1; }
  rm -rf "$FIX"
}

@test "release fails silently on stdout when the patch fails" {
  new_fixture
  kcp_with "[\"$HOLD\"]"
  echo 1 >"$FIX/patch-rc"
  rc=0
  out=$(kh kcp_release) || rc=$?
  [ "$rc" -eq 1 ] || { echo "rc=$rc, want 1" >&2; exit 1; }
  [ -z "$out" ] || { echo "printed '$out' on a failed release" >&2; exit 1; }
  rm -rf "$FIX"
}

@test "release fails when the object cannot be read as JSON" {
  new_fixture
  echo 'not json' >"$FIX/kcp.json"
  if kh kcp_release; then echo "release reported the finalizer gone without reading the object" >&2; exit 1; fi
  rm -rf "$FIX"
}

@test "release fails when the read fails" {
  new_fixture
  kcp_with "[\"$HOLD\"]"
  echo 1 >"$FIX/get-rc"
  if kh kcp_release; then echo "release reported the finalizer gone after a failed read" >&2; exit 1; fi
  [ ! -f "$FIX/patch" ] || { echo "patched after a failed read" >&2; exit 1; }
  rm -rf "$FIX"
}
