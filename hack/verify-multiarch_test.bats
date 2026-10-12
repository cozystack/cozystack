#!/usr/bin/env bats
# Tests for hack/verify-multiarch.sh, the release gate that every pinned image
# is an index serving both linux/amd64 and linux/arm64.
#
# The registry is a stub: skopeo on PATH answers `inspect --raw` from one file
# per digest, so each test decides what every ref resolves to.
#
# Harness note: the unit lane runs this file under Bats. The bodies stay POSIX
# sh (no `run`, no [[ ]]; each @test a shell function under `set -eu`), so
# hack/cozytest.sh can still run it under dash by hand.
#
# Run with: bats hack/verify-multiarch_test.bats

load test_helper

_stub_registry() {
  MOCK_REG="$1"
  export MOCK_REG
  mkdir -p "$MOCK_REG/bin" "$MOCK_REG/blobs"
  : >"$MOCK_REG/log"
  : >"$MOCK_REG/tags"
  cat >"$MOCK_REG/bin/skopeo" <<'EOF'
#!/bin/sh
set -eu
printf 'skopeo %s\n' "$*" >>"$MOCK_REG/log"
[ "$1 $2" = "inspect --raw" ] || exit 2
# MOCK_THROTTLE=N: the first N lookups are refused as a rate-limited
# registry refuses them.
if [ -n "${MOCK_THROTTLE:-}" ] && [ "$(grep -c 'inspect --raw' "$MOCK_REG/log")" -le "$MOCK_THROTTLE" ]; then
  echo "Error: reading manifest: toomanyrequests: You have reached your pull rate limit" >&2
  exit 1
fi
r="${3#docker://}"
case "$r" in
  *:*@sha256:*) echo "Error: a tag and a digest together: $r" >&2; exit 2 ;;
  *@sha256:*) ;;
  # "<repo>:<tag>" resolves through the tags table, as a registry would.
  *) r="x@$(awk -v r="$r" '$1 == r { d = $2 } END { printf "%s", d }' "$MOCK_REG/tags")" ;;
esac
f="$MOCK_REG/blobs/${r##*@sha256:}"
[ -f "$f" ] || { echo "Error: reading manifest ${r##*@}: manifest unknown" >&2; exit 1; }
cat "$f"
EOF
  chmod +x "$MOCK_REG/bin/skopeo"
}

# _blob <body> stores a manifest and prints its digest.
_blob() {
  hex="$(printf '%s' "$1" | sha256sum | cut -d' ' -f1)"
  printf '%s' "$1" >"$MOCK_REG/blobs/$hex"
  printf 'sha256:%s' "$hex"
}

# _index <arch>... prints the digest of an index with one linux entry per arch,
# plus the attestation entry buildx adds.
_index() {
  entries='{"digest":"sha256:0","platform":{"os":"unknown","architecture":"unknown"}}'
  for a in "$@"; do
    entries="$entries,{\"digest\":\"sha256:$a\",\"platform\":{\"os\":\"linux\",\"architecture\":\"$a\"}}"
  done
  _blob "{\"mediaType\":\"application/vnd.oci.image.index.v1+json\",\"manifests\":[$entries]}"
}

_single() {
  _blob "{\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"config\":{\"mediaType\":\"application/vnd.oci.image.config.v1+json\"},\"name\":\"$1\"}"
}

# A tree with a string ref, two split-map refs, a ref behind a --flag= prefix,
# a .tag file and the packages artifact, every image a two-arch index.
_make_tree() {
  t="$1/tree"
  mkdir -p "$t/system/api" "$t/system/cil" "$t/system/lin" "$t/system/kamaji" "$t/apps/kube/images" "$t/core/installer"
  printf 'image: ghcr.io/cozy/api:v1@%s\n' "$(_index amd64 arm64)" >"$t/system/api/values.yaml"
  printf 'cilium:\n  image:\n    repository: quay.io/cilium/cilium\n    tag: v1.0\n    digest: %s\n' \
    "$(_index amd64 arm64)" >"$t/system/cil/values.yaml"
  # The enumeration also emits this map's bare "v2@<digest>" tag value.
  printf 'image:\n  repository: ghcr.io/cozy/lin\n  tag: v2@%s\n' "$(_index amd64 arm64)" >"$t/system/lin/values.yaml"
  printf 'extraArgs:\n  - --migrate-image=ghcr.io/cozy/kamaji:v1@%s\n' "$(_index arm64 amd64)" >"$t/system/kamaji/values.yaml"
  printf 'ghcr.io/cozy/ubuntu:v1@%s\n' "$(_index amd64 arm64 s390x)" >"$t/apps/kube/images/ubuntu.tag"
  # Not an image: the gate must not ask it for platforms.
  printf 'platformSourceUrl: oci://ghcr.io/cozy/cozystack-packages\nplatformSourceRef: digest=%s\n' \
    "$(_blob '{"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.cncf.flux.config.v1+json"}')" \
    >"$t/core/installer/values.yaml"
  : >"$1/allowlist"
}

@test "a tree of two-arch indexes passes, flag-prefixed refs included" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_tree "$tmp"
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/out"
  grep -q 'ghcr.io/cozy/kamaji@sha256:' "$MOCK_REG/log"
  if grep -q 'migrate-image' "$MOCK_REG/log"; then echo "FAIL: the --flag= prefix reached the registry"; false; fi
  if grep -q 'cozystack-packages' "$MOCK_REG/log"; then echo "FAIL: the packages artifact was inspected"; false; fi
  # One lookup per image: the bare tag half of the lin map is not a ref.
  [ "$(grep -c 'inspect --raw' "$MOCK_REG/log")" -eq 5 ]
  rm -rf "$tmp"
}

@test "every single-arch ref fails the gate and is named with its reason" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_tree "$tmp"
  single=$(_single api)
  amd_only=$(_index amd64)
  printf 'image: ghcr.io/cozy/api:v1@%s\n' "$single" >"$tmp/tree/system/api/values.yaml"
  printf 'extraArgs:\n  - --migrate-image=ghcr.io/cozy/kamaji:v1@%s\n' "$amd_only" >"$tmp/tree/system/kamaji/values.yaml"
  printf 'ghcr.io/cozy/ubuntu:v1@sha256:%s\n' "$(printf 'f%.0s' $(seq 1 64))" >"$tmp/tree/apps/kube/images/ubuntu.tag"
  # A digest seen only without a host still has to resolve, or fail.
  printf 'image:\n  tag: v3@sha256:%s\n' "$(printf 'e%.0s' $(seq 1 64))" >"$tmp/tree/system/lin/values.yaml"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  grep -q "ghcr.io/cozy/api:v1@$single: single-platform manifest" "$tmp/out"
  grep -q "ghcr.io/cozy/kamaji:v1@$amd_only: index without linux/arm64" "$tmp/out"
  grep -q "ghcr.io/cozy/ubuntu:v1@sha256:f*: cannot read the manifest" "$tmp/out"
  grep -q "^  v3@sha256:e*: cannot read the manifest" "$tmp/out"
  if grep -q 'quay.io/cilium' "$tmp/out"; then echo "FAIL: a passing ref was listed"; false; fi

  # --report prints the same list and does not fail.
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --report --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/report" 2>&1
  diff "$tmp/out" "$tmp/report"
  rm -rf "$tmp"
}

@test "an allowlisted repository passes as allowed single-arch, and a stale entry is named" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_tree "$tmp"
  single=$(_single api)
  printf 'image: ghcr.io/cozy/api:v1@%s\n' "$single" >"$tmp/tree/system/api/values.yaml"
  printf '# header\n\nghcr.io/cozy/api  # amd64 drivers only\nghcr.io/cozy/gone  # removed image\n' >"$tmp/allowlist"
  # A stale entry is a warning: the run still exits 0.
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/out" 2>&1
  grep -q '^refs that failed (0):' "$tmp/out"
  sed -n '/^allowed single-arch/,/^$/p' "$tmp/out" | grep -q "ghcr.io/cozy/api:v1@$single: single-platform manifest (amd64 drivers only)"
  sed -n '/^stale allowlist/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/gone'
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --report --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/report" 2>&1
  diff "$tmp/out" "$tmp/report"

  # The entry covers its repository only: another single-arch image still fails.
  printf 'image: ghcr.io/cozy/other:v1@%s\n' "$(_single other)" >"$tmp/tree/system/cil/values.yaml"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  sed -n '/^refs that failed/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/other:v1@'
  if sed -n '/^refs that failed/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/api'; then echo "FAIL: the allowlisted ref was counted as a failure"; false; fi

  # The allowlist excuses a platform verdict only. An allowlisted ref that
  # cannot be read, or stays rate-limited, is broken on amd64 as well.
  printf 'image: ghcr.io/cozy/other:v1@%s\n' "$(_index amd64 arm64)" >"$tmp/tree/system/cil/values.yaml"
  printf 'image: ghcr.io/cozy/api:v1@sha256:%s\n' "$(printf 'd%.0s' $(seq 1 64))" >"$tmp/tree/system/api/values.yaml"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  sed -n '/^refs that failed/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/api:v1@sha256:d*: cannot read the manifest'
  if sed -n '/^allowed single-arch/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/api'; then echo "FAIL: an unreadable ref was allowed"; false; fi
  if sed -n '/^stale allowlist/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/api'; then echo "FAIL: a pinned allowlisted repository was called stale"; false; fi
  : >"$MOCK_REG/log"
  printf 'image: ghcr.io/cozy/api:v1@%s\n' "$single" >"$tmp/tree/system/api/values.yaml"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" MOCK_THROTTLE=99 VERIFY_MULTIARCH_RETRY_DELAY=0 \
    hack/verify-multiarch.sh --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  sed -n '/^refs that failed/,/^$/p' "$tmp/out" | grep -q "ghcr.io/cozy/api:v1@$single: rate-limited"
  if sed -n '/^stale allowlist/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/api'; then echo "FAIL: a rate-limited allowlisted repository was called stale"; false; fi

  # Once the allowlisted repository's pins all pass, its entry is stale.
  printf 'image: ghcr.io/cozy/api:v1@%s\n' "$(_index amd64 arm64)" >"$tmp/tree/system/api/values.yaml"
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/out" 2>&1
  sed -n '/^stale allowlist/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/api'
  rm -rf "$tmp"
}

@test "an allowlist entry without a reason is refused" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_tree "$tmp"
  printf 'ghcr.io/cozy/api\n' >"$tmp/allowlist"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --report --allowlist "$tmp/allowlist" "$tmp/tree" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 2 ]
  grep -q 'ghcr.io/cozy/api.*reason' "$tmp/out"
  rm -rf "$tmp"
}

@test "the committed allowlist gives every entry a reason" {
  [ -f hack/multiarch-allowlist ]
  if grep -vE '^[[:space:]]*(#|$)' hack/multiarch-allowlist | grep -vE '^[^[:space:]#]+[[:space:]]+#[[:space:]]*[^[:space:]]'; then
    echo "FAIL: an entry above has no reason comment"; false
  fi
}

@test "--refs-file checks a list of refs, tag-only ones resolved by tag" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  : >"$tmp/allowlist"
  both=$(_index amd64 arm64)
  printf 'ghcr.io/cozy/multi:v1 %s\n' "$both" >>"$MOCK_REG/tags"
  single=$(_single one)
  printf 'ghcr.io/cozy/one:v1 %s\n' "$single" >>"$MOCK_REG/tags"
  # Duplicates, and the same image reached by tag and by digest, as a node's
  # image store lists it.
  printf 'ghcr.io/cozy/multi:v1\nghcr.io/cozy/multi:v1\nghcr.io/cozy/multi@%s\n\n' "$both" >"$tmp/refs"
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" --refs-file "$tmp/refs" >"$tmp/out" 2>&1
  grep -q '^refs that failed (0):' "$tmp/out"
  [ "$(grep -c 'inspect --raw docker://ghcr.io/cozy/multi:v1$' "$MOCK_REG/log")" -eq 1 ]
  [ "$(grep -c 'inspect --raw' "$MOCK_REG/log")" -eq 2 ]

  printf 'ghcr.io/cozy/one:v1\n' >>"$tmp/refs"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" --refs-file "$tmp/refs" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  grep -q '^  ghcr.io/cozy/one:v1: single-platform manifest$' "$tmp/out"
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --report --allowlist "$tmp/allowlist" --refs-file "$tmp/refs" >"$tmp/report" 2>&1
  diff "$tmp/out" "$tmp/report"

  # The allowlist applies to a list of refs as it does to a tree. An entry that
  # matches nothing is not stale here: a list of pulled images is not the tree,
  # and an image the run never pulled says nothing about the entry.
  printf 'ghcr.io/cozy/one  # amd64 drivers only\nghcr.io/cozy/unpulled  # never pulled here\n' >"$tmp/allowlist"
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --allowlist "$tmp/allowlist" --refs-file "$tmp/refs" >"$tmp/out" 2>&1
  sed -n '/^allowed single-arch/,/^$/p' "$tmp/out" | grep -q 'ghcr.io/cozy/one:v1: single-platform manifest (amd64 drivers only)'
  if grep -q 'stale' "$tmp/out"; then echo "FAIL: --refs-file reported a stale entry"; false; fi

  # An empty list is an error, not a pass: the collection produced nothing.
  : >"$tmp/empty"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" hack/verify-multiarch.sh --report --allowlist "$tmp/allowlist" --refs-file "$tmp/empty" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 2 ]
  rm -rf "$tmp"
}

@test "a rate-limited lookup is retried, and one still refused is named as rate-limited" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  : >"$tmp/allowlist"
  printf 'ghcr.io/cozy/multi@%s\n' "$(_index amd64 arm64)" >"$tmp/refs"
  # Refused twice, answered on the third try.
  PATH="$MOCK_REG/bin:$PATH" MOCK_THROTTLE=2 VERIFY_MULTIARCH_RETRY_DELAY=0 \
    hack/verify-multiarch.sh --allowlist "$tmp/allowlist" --refs-file "$tmp/refs" >"$tmp/out" 2>&1
  grep -q '^refs that failed (0):' "$tmp/out"
  [ "$(grep -c 'inspect --raw' "$MOCK_REG/log")" -eq 3 ]

  # Refused every time: a failure that says so, never a platform verdict.
  : >"$MOCK_REG/log"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" MOCK_THROTTLE=99 VERIFY_MULTIARCH_RETRY_DELAY=0 \
    hack/verify-multiarch.sh --allowlist "$tmp/allowlist" --refs-file "$tmp/refs" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  grep -q '^  ghcr.io/cozy/multi@sha256:[0-9a-f]*: rate-limited or unavailable after 3 tries' "$tmp/out"
  if grep -q 'single-platform\|cannot read the manifest' "$tmp/out"; then echo "FAIL: a rate limit read as a verdict"; false; fi
  [ "$(grep -c 'inspect --raw' "$MOCK_REG/log")" -eq 3 ]

  # A missing manifest is deterministic: one lookup, no retry.
  : >"$MOCK_REG/log"
  printf 'ghcr.io/cozy/gone@sha256:%s\n' "$(printf 'e%.0s' $(seq 1 64))" >"$tmp/refs"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" VERIFY_MULTIARCH_RETRY_DELAY=0 \
    hack/verify-multiarch.sh --allowlist "$tmp/allowlist" --refs-file "$tmp/refs" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  grep -q 'gone@sha256:e*: cannot read the manifest: .*manifest unknown' "$tmp/out"
  [ "$(grep -c 'inspect --raw' "$MOCK_REG/log")" -eq 1 ]
  rm -rf "$tmp"
}

@test "the nightly arm64 build reports main's refs before its build stamps the tree" {
  wf=.github/workflows/build-arm64-nightly.yaml
  names=$(yq -r '.jobs.build-arm64.steps[].name' "$wf")
  r=$(echo "$names" | grep -nx 'Report refs that are not multi-arch' | cut -d: -f1)
  b=$(echo "$names" | grep -nx 'Build arm64 images' | cut -d: -f1)
  [ -n "$r" ] && [ -n "$b" ] && [ "$r" -lt "$b" ]
  [ "$(yq -r '.jobs.build-arm64.steps[] | select(.name == "Report refs that are not multi-arch") | .run' "$wf")" = 'hack/verify-multiarch.sh --report packages' ]
  [ "$(yq -r '.jobs.build-arm64.steps[] | select(.name == "Build arm64 images") | .env.WRITE_CACHE' "$wf")" = 1 ]
}
