#!/usr/bin/env bats
# Behavioural tests for the stable-candidate packages artifact publisher.
# Run with: hack/cozytest.sh hack/promote-packages-artifact_test.bats

_test_workspace() {
  if [ -n "${BATS_TEST_TMPDIR:-}" ]; then
    printf '%s\n' "$BATS_TEST_TMPDIR"
  else
    printf '%s\n' "${tmp:?cozytest workspace is missing}"
  fi
}

@test "publishes a reproducible stable candidate and pins its digest" {
  tmp="$(_test_workspace)/publisher"
  mkdir -p "$tmp/bin" "$tmp/packages/core/installer"
  cat > "$tmp/bin/flux" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$@" > "$MOCK_FLUX_LOG"
printf '%s\n' "$MOCK_FLUX_RESULT"
EOF
  chmod +x "$tmp/bin/flux"
  # The rc tree pins the trusted repository by digest, which is what the
  # preflight demands; REGISTRY sends the candidate somewhere else so the
  # rewrite assertions below still have something to prove.
  cat > "$tmp/packages/core/installer/values.yaml" <<'EOF'
cozystackOperator:
  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/cozystack-packages
  platformSourceRef: digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
EOF
  digest='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
  sha='cccccccccccccccccccccccccccccccccccccccc'

  rc=0
  REGISTRY=example.com/cozystack SOURCE_URL=https://example.com/cozystack \
    PROMOTION_ID=123456789-2 \
    MOCK_FLUX_LOG="$tmp/flux.log" MOCK_FLUX_RESULT="{\"digest\":\"$digest\"}" \
    PATH="$tmp/bin:$PATH" \
    hack/promote-packages-artifact.sh 9.9.9 v9.9.9-rc.3 "$sha" "$tmp/packages" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "publisher exited $rc" >&2
    cat "$tmp/err" >&2
    return "$rc"
  fi

  grep -qx 'oci://example.com/cozystack/cozystack-packages:promotion-v9.9.9-from-v9.9.9-rc.3-run-123456789-2' "$tmp/flux.log"
  grep -qx -- '--reproducible' "$tmp/flux.log"
  grep -qx -- '--output' "$tmp/flux.log"
  grep -qx 'json' "$tmp/flux.log"
  grep -qx -- '--revision=promotion-v9.9.9-from-v9.9.9-rc.3@sha1:'"$sha" "$tmp/flux.log"
  grep -qx -- '--source=https://example.com/cozystack' "$tmp/flux.log"
  [ "$(yq -r '.cozystackOperator.platformSourceUrl' "$tmp/packages/core/installer/values.yaml")" = 'oci://example.com/cozystack/cozystack-packages' ]
  [ "$(yq -r '.cozystackOperator.platformSourceRef' "$tmp/packages/core/installer/values.yaml")" = "digest=$digest" ]
  grep -q 'and pinned installer values' "$tmp/out"
}

@test "propagates a Flux push failure without changing installer values" {
  tmp="$(_test_workspace)/publisher"
  mkdir -p "$tmp/bin" "$tmp/packages/core/installer"
  cat > "$tmp/bin/flux" <<'EOF'
#!/bin/sh
exit 42
EOF
  chmod +x "$tmp/bin/flux"
  cat > "$tmp/packages/core/installer/values.yaml" <<'EOF'
cozystackOperator:
  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/cozystack-packages
  platformSourceRef: digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
EOF
  cp "$tmp/packages/core/installer/values.yaml" "$tmp/before"
  sha='ffffffffffffffffffffffffffffffffffffffff'

  rc=0
  PROMOTION_ID=123456789-3 PATH="$tmp/bin:$PATH" \
    hack/promote-packages-artifact.sh 9.9.9 v9.9.9-rc.1 "$sha" "$tmp/packages" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -eq 42 ]
  cmp -s "$tmp/before" "$tmp/packages/core/installer/values.yaml"
}

@test "rejects an rc from a different stable version before publishing" {
  tmp="$(_test_workspace)/publisher"
  mkdir -p "$tmp/packages/core/installer"
  : > "$tmp/packages/core/installer/values.yaml"
  sha='dddddddddddddddddddddddddddddddddddddddd'

  rc=0
  PROMOTION_ID=123456789-4 \
    hack/promote-packages-artifact.sh 9.9.9 v9.9.8-rc.1 "$sha" "$tmp/packages" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q "must match v9.9.9-rc.N" "$tmp/err"
}

# A mock that records the fact it ran at all, for the preflights below: their
# whole point is that nothing reaches the registry, and "the script exited
# non-zero" does not say that on its own.
_make_recording_flux() {
  mkdir -p "$1/bin"
  cat > "$1/bin/flux" <<'EOF'
#!/bin/sh
printf 'invoked\n' > "$MOCK_FLUX_LOG"
printf '%s\n' '{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}'
EOF
  chmod +x "$1/bin/flux"
}

# The candidate is pushed BEFORE the rewrite at the end of the script, so it
# carries the rc tree's pin verbatim and verify-promoted-packages.sh reads it
# back as the candidate's rc baseline. Both preflights below therefore bring a
# verification failure forward: without them the artifact, the staging branch
# and the draft release all exist by the time the same values are rejected.
@test "rejects an rc tree pinned by tag before publishing" {
  tmp="$(_test_workspace)/publisher"
  mkdir -p "$tmp/packages/core/installer"
  _make_recording_flux "$tmp"
  cat > "$tmp/packages/core/installer/values.yaml" <<'EOF'
cozystackOperator:
  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/cozystack-packages
  platformSourceRef: tag=v9.9.9-rc.3
EOF
  sha='1111111111111111111111111111111111111111'

  rc=0
  PROMOTION_ID=123456789-6 MOCK_FLUX_LOG="$tmp/flux.log" PATH="$tmp/bin:$PATH" \
    hack/promote-packages-artifact.sh 9.9.9 v9.9.9-rc.3 "$sha" "$tmp/packages" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q "rc tree's platformSourceRef 'tag=v9.9.9-rc.3' is not an immutable digest" "$tmp/err"
  [ ! -e "$tmp/flux.log" ]
}

@test "rejects an rc tree pinned outside the trusted repository before publishing" {
  tmp="$(_test_workspace)/publisher"
  mkdir -p "$tmp/packages/core/installer"
  _make_recording_flux "$tmp"
  cat > "$tmp/packages/core/installer/values.yaml" <<'EOF'
cozystackOperator:
  platformSourceUrl: oci://example.invalid/cozystack-packages
  platformSourceRef: digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
EOF
  sha='2222222222222222222222222222222222222222'

  rc=0
  PROMOTION_ID=123456789-7 MOCK_FLUX_LOG="$tmp/flux.log" PATH="$tmp/bin:$PATH" \
    hack/promote-packages-artifact.sh 9.9.9 v9.9.9-rc.3 "$sha" "$tmp/packages" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q "rc tree's platformSourceUrl 'oci://example.invalid/cozystack-packages' must equal trusted repository" "$tmp/err"
  [ ! -e "$tmp/flux.log" ]
}

# PROMOTION_ID is not attacker-influenced — the workflow builds it from
# github.run_id and github.run_attempt — but it is the run-uniqueness suffix of
# the candidate tag, and retention.yaml only ever deletes a cozystack-packages
# version whose every tag matches `promotion-v…-run-[0-9]+-[0-9]+`. A value
# that fails that shape publishes a candidate no cleanup pass can ever collect,
# so the two patterns are a contract and this refusal is the end of it that a
# test can reach.
@test "rejects a malformed PROMOTION_ID before publishing" {
  tmp="$(_test_workspace)/publisher"
  mkdir -p "$tmp/packages/core/installer"
  _make_recording_flux "$tmp"
  cat > "$tmp/packages/core/installer/values.yaml" <<'EOF'
cozystackOperator:
  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/cozystack-packages
  platformSourceRef: digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
EOF
  sha='3333333333333333333333333333333333333333'

  rc=0
  PROMOTION_ID='123456789/2' MOCK_FLUX_LOG="$tmp/flux.log" PATH="$tmp/bin:$PATH" \
    hack/promote-packages-artifact.sh 9.9.9 v9.9.9-rc.3 "$sha" "$tmp/packages" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q "must match <run-id>-<run-attempt>" "$tmp/err"
  [ ! -e "$tmp/flux.log" ]
}

@test "refuses an invalid digest without changing installer values" {
  tmp="$(_test_workspace)/publisher"
  mkdir -p "$tmp/bin" "$tmp/packages/core/installer"
  cat > "$tmp/bin/flux" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' '{"digest":"sha256:short"}'
EOF
  chmod +x "$tmp/bin/flux"
  cat > "$tmp/packages/core/installer/values.yaml" <<'EOF'
cozystackOperator:
  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/cozystack-packages
  platformSourceRef: digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
EOF
  cp "$tmp/packages/core/installer/values.yaml" "$tmp/before"
  sha='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'

  rc=0
  PROMOTION_ID=123456789-5 PATH="$tmp/bin:$PATH" \
    hack/promote-packages-artifact.sh 9.9.9 v9.9.9-rc.1 "$sha" "$tmp/packages" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'invalid artifact digest' "$tmp/err"
  cmp -s "$tmp/before" "$tmp/packages/core/installer/values.yaml"
}
