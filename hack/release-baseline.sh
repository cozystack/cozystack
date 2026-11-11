#!/usr/bin/env bash
# The nightly baseline of the release lines (docs/release/nightly-builds.md):
# every line the backport batch runs for is tested at one pinned head, with its
# own unit lane and the full e2e suite against its own cozystack-packages
# artifact, so a red batch pull request can be told apart from a line that was
# red already. .github/workflows/nightly.yaml runs it; a maintainer can run
# each command by hand from a worktree of the repository.
#
# Usage: release-baseline.sh <command> [arguments]
#
#   discover
#       Prints a JSON array with one entry per line `backport-batch lines`
#       prints, in its order: {line, sha, state, digest, revision}. sha is
#       the line's head on origin, read once here; every later job tests that
#       commit. state, digest and revision are those of `artifact`.
#   artifact LINE SHA
#       Prints state=, digest= and revision= for cozystack-packages:LINE.
#       state is fresh when the artifact was built from SHA, or from an
#       ancestor whose whole difference to SHA lies in paths the push filter
#       of SHA's build-release.yaml ignores; stale otherwise; missing when the
#       registry has no such tag.
#   verify DIGEST SHA
#       Exits 0 when cozystack-packages@DIGEST is fresh for SHA as above.
#   stage DIGEST
#       Overlays the artifact's tree on ./packages and points the installer at
#       it, as the main nightly stages its own tree.
#   unit-commands
#       Prints the make commands the line's own pull-request unit job runs,
#       one per line, read from its .github/workflows/pull-requests.yaml.
#   toolchain [MAIN_DIR]
#       Prints the tool pins of that job as KEY=VALUE lines. A helm-unittest
#       version the line leaves floating is taken from MAIN_DIR's workflow,
#       because no workflow on main may install the plugin unpinned.
#   record FILE KEY=VALUE...
#       Writes one leg's result: kind, line, sha, result, digest, rebuilt.
#       junit=PATH adds tests=<number of test cases in PATH>.
#   report MATRIX_FILE RESULTS_DIR
#       Prints a markdown summary per line from the discover output and the
#       recorded results, and exits 1 unless every line's unit and e2e passed.
#
# Every command but discover, record and report reads the git tree of the
# current directory: discover needs a checkout with the lines fetched, verify
# and artifact one that has both commits, stage, unit-commands and toolchain a
# checkout of the line itself.
#
# Environment:
#   BACKPORT_BATCH   the backport-batch binary (default: built from this tree)
#   REGISTRY         where cozystack-packages lives (default: the CI registry)
#
# Exits 0 on success, 1 when a check fails, and 2 on a usage or setup error.
set -euo pipefail

REGISTRY=${REGISTRY:-iad.ocir.io/idyksih5sir9/cozystack}
PACKAGES="${REGISTRY}/cozystack-packages"

die() {
  echo "release-baseline: $*" >&2
  exit 2
}

fail() {
  echo "release-baseline: $*" >&2
  exit 1
}

is_sha() {
  [[ $1 =~ ^[0-9a-f]{40}$ ]]
}

is_digest() {
  [[ $1 =~ ^sha256:[0-9a-f]{64}$ ]]
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Reads cozystack-packages$1 (":<tag>" or "@<digest>") into DIGEST and
# REVISION. Returns 3 when the registry does not know the manifest; any other
# registry error stops the script, so an outage never reads as "missing" and
# never sends a line to a rebuild it does not need.
inspect() {
  local ref=$1 rev
  if ! skopeo inspect --raw "docker://${PACKAGES}${ref}" > "$tmp/manifest" 2> "$tmp/err"; then
    if grep -qi 'manifest unknown' "$tmp/err"; then
      return 3
    fi
    cat "$tmp/err" >&2
    fail "cannot read cozystack-packages${ref} from ${REGISTRY}"
  fi
  # --raw and manifest-digest for the reasons nightly.yaml gives: the artifact
  # is a Flux OCI artifact, not an image.
  DIGEST=$(skopeo manifest-digest "$tmp/manifest")
  # The revision is "<git describe>:<sha>"
  # (packages/core/installer/Makefile, image-packages).
  rev=$(jq -r '.annotations["org.opencontainers.image.revision"] // ""' "$tmp/manifest" 2> /dev/null || true)
  REVISION=${rev##*:}
}

# Exits 0 when an artifact built at revision $1 is what build-release would
# still publish for $2: the same commit, or an ancestor whose changes all fall
# under the push filter's paths-ignore. Only `<dir>/**` patterns are read; any
# other pattern ignores nothing here, which can cost a rebuild but never
# passes a stale artifact.
built_for() {
  local rev=$1 sha=$2 pattern file
  local -a prefixes=()
  [ "$rev" = "$sha" ] && return 0
  is_sha "$rev" || return 1
  git cat-file -e "${rev}^{commit}" 2> /dev/null || return 1
  git merge-base --is-ancestor "$rev" "$sha" || return 1
  git show "${sha}:.github/workflows/build-release.yaml" > "$tmp/build-release.yaml" 2> /dev/null || return 1
  while IFS= read -r pattern; do
    case "$pattern" in
      */'**')
        case "${pattern%'**'}" in
          *[*?[!]*) ;;
          *) prefixes+=("${pattern%'**'}") ;;
        esac
        ;;
    esac
  done < <(yq -r '.on.push["paths-ignore"][]?' "$tmp/build-release.yaml")
  # --no-renames lists both sides of a rename, so a file moved from code into
  # docs/ still counts as a code change.
  git diff --no-renames --name-only "$rev" "$sha" > "$tmp/changed"
  while IFS= read -r file; do
    for pattern in ${prefixes[@]+"${prefixes[@]}"}; do
      [[ $file == "$pattern"* ]] && continue 2
    done
    return 1
  done < "$tmp/changed"
  return 0
}

# Sets STATE, DIGEST and REVISION for cozystack-packages:$1 against $2.
artifact_state() {
  local line=$1 sha=$2 rc=0
  DIGEST='' REVISION=''
  inspect ":$line" || rc=$?
  if [ "$rc" -eq 3 ]; then
    STATE=missing
  elif built_for "$REVISION" "$sha"; then
    STATE=fresh
  else
    STATE=stale
  fi
}

cmd_discover() {
  [ $# -eq 0 ] || die "usage: discover"
  local bin out line sha json='[]' seen=' '
  bin=${BACKPORT_BATCH:-}
  if [ -z "$bin" ]; then
    bin="$tmp/backport-batch"
    go build -o "$bin" ./cmd/backport-batch || die "cannot build backport-batch"
  fi
  out=$("$bin" lines) || fail "backport-batch lines failed"
  [ -n "$out" ] || fail "backport-batch lines printed no line"
  while IFS= read -r line; do
    [[ $line =~ ^release-[0-9]+\.[0-9]+$ ]] \
      || fail "backport-batch lines printed '$line', which is not a release-X.Y line"
    [[ $seen != *" $line "* ]] || fail "backport-batch lines printed $line twice"
    seen+="$line "
    sha=$(git rev-parse --verify --quiet "refs/remotes/origin/${line}^{commit}") \
      || fail "$line has no head on origin"
    is_sha "$sha" || fail "$line resolves to '$sha', not a commit"
    artifact_state "$line" "$sha"
    json=$(jq -c --arg line "$line" --arg sha "$sha" \
      --arg state "$STATE" --arg digest "$DIGEST" --arg revision "$REVISION" \
      '. + [{line: $line, sha: $sha, state: $state, digest: $digest, revision: $revision}]' <<< "$json")
  done <<< "$out"
  printf '%s\n' "$json"
}

cmd_artifact() {
  [ $# -eq 2 ] || die "usage: artifact LINE SHA"
  [[ $1 =~ ^release-[0-9]+\.[0-9]+$ ]] || die "'$1' is not a release-X.Y line"
  is_sha "$2" || die "'$2' is not a commit"
  artifact_state "$1" "$2"
  printf 'state=%s\ndigest=%s\nrevision=%s\n' "$STATE" "$DIGEST" "$REVISION"
}

cmd_verify() {
  [ $# -eq 2 ] || die "usage: verify DIGEST SHA"
  local digest=$1 sha=$2 rc=0
  is_digest "$digest" || fail "'$digest' is not a sha256 digest"
  is_sha "$sha" || die "'$sha' is not a commit"
  DIGEST='' REVISION=''
  inspect "@$digest" || rc=$?
  [ "$rc" -ne 3 ] || fail "${REGISTRY} has no cozystack-packages@$digest"
  [ "$DIGEST" = "$digest" ] || fail "the registry served $DIGEST for $digest"
  built_for "$REVISION" "$sha" \
    || fail "cozystack-packages@$digest was built from '${REVISION}', not from $sha or an ancestor it can stand in for"
  echo "cozystack-packages@$digest was built from $REVISION"
}

cmd_stage() {
  [ $# -eq 1 ] || die "usage: stage DIGEST"
  local digest=$1
  is_digest "$digest" || fail "'$digest' is not a sha256 digest"
  [ -d packages ] || die "no packages/ here: run stage from a checkout of the line"
  # flux refuses an --output directory that does not exist yet.
  mkdir -p "$tmp/tree"
  flux pull artifact "oci://${PACKAGES}@${digest}" --output "$tmp/tree"
  cp -r "$tmp/tree/." packages/
  URL="oci://${PACKAGES}" REF="digest=${digest}" yq -i \
    '.cozystackOperator.platformSourceUrl = strenv(URL) | .cozystackOperator.platformSourceRef = strenv(REF)' \
    packages/core/installer/values.yaml
}

# The steps of the line's own `checks` job, as JSON.
checks_job() {
  local wf=.github/workflows/pull-requests.yaml
  [ -f "$wf" ] || die "no $wf here: run this from a checkout of the line"
  yq -o=json '.jobs.checks' "$wf"
}

cmd_unit_commands() {
  [ $# -eq 0 ] || die "usage: unit-commands"
  local cmds cmd
  # A step runs on a code change when it has no condition or asks for one;
  # the docs-only Bats step of newer lines asks for the opposite and is left
  # out, since the line's unit-tests goal runs Bats anyway.
  cmds=$(checks_job | jq -r '
    .steps[] | select(.run != null)
    | select(.if == null or (.if | test("outputs\\.code == .true.")))
    | .run | rtrimstr("\n") | select(startswith("make "))')
  [ -n "$cmds" ] || fail "the line's checks job runs no make command"
  # Each command is run word-split, so it may only carry make goals, flags
  # and variable assignments.
  while IFS= read -r cmd; do
    [[ $cmd =~ ^make( [A-Za-z0-9_.=/+:-]+)+$ ]] || fail "refusing to run '$cmd' from the line's checks job"
  done <<< "$cmds"
  grep -Eq '(^| )unit-tests( |$)' <<< "$cmds" || fail "the line's checks job does not run make unit-tests"
  printf '%s\n' "$cmds"
}

cmd_toolchain() {
  [ $# -le 1 ] || die "usage: toolchain [MAIN_DIR]"
  local job key value main_job='' out=''
  job=$(checks_job)
  if [ -n "${1:-}" ]; then
    main_job=$(cd "$1" && checks_job)
  fi
  # The step's env, then the job's. Checked inside jq, before -r can turn an
  # embedded newline into a second line: these lines go to GITHUB_ENV.
  # shellcheck disable=SC2016 # a jq program; $k is jq's, not the shell's
  local pick='
    first((.steps[]? | select(.name == "Set up test toolchain") | .env[$k]?), .env[$k]?
          | select(. != null) | tostring) // ""
    | if test("^[A-Za-z0-9._:/|-]*$") then . else error("refusing \($k)=\(.)") end'
  for key in HELM_UNITTEST_VERSION BATS_VERSION BATS_COMMIT GOPROXY; do
    value=$(jq -r --arg k "$key" "$pick" <<< "$job") || fail "the line's checks job carries an unusable $key"
    if [ -z "$value" ] && [ "$key" = HELM_UNITTEST_VERSION ] && [ -n "$main_job" ]; then
      value=$(jq -r --arg k "$key" "$pick" <<< "$main_job") || fail "main's checks job carries an unusable $key"
      [ -n "$value" ] || fail "main's checks job pins no $key"
    fi
    out+="$key=$value"$'\n'
  done
  # All or nothing: a refusal must not leave half the pins in GITHUB_ENV.
  printf '%s' "$out"
}

cmd_record() {
  [ $# -ge 1 ] || die "usage: record FILE KEY=VALUE..."
  local file=$1 kv key value tests
  shift
  : > "$file.tmp"
  for kv in "$@"; do
    key=${kv%%=*}
    value=${kv#*=}
    [[ $value != *$'\n'* ]] || die "value of $key spans lines"
    case "$key" in
      kind | line | sha | result | digest | rebuilt) printf '%s=%s\n' "$key" "$value" >> "$file.tmp" ;;
      junit)
        tests=0
        if [ -f "$value" ]; then
          tests=$({ grep -o '<testcase[ >/]' "$value" || true; } | wc -l | tr -d ' ')
        fi
        printf 'tests=%s\n' "$tests" >> "$file.tmp"
        ;;
      *) die "unknown key '$key'" ;;
    esac
  done
  mv "$file.tmp" "$file"
}

# The value of $2 in the result file $1, empty when either is missing.
field() {
  [ -f "$1" ] || return 0
  sed -n "s/^$2=//p" "$1" | head -n 1
}

cmd_report() {
  [ $# -eq 2 ] || die "usage: report MATRIX_FILE RESULTS_DIR"
  local matrix=$1 results=$2 lines line sha state unit e2e tests digest rebuilt verdict red=0 run_url
  lines=$(jq -r 'if type == "array" then .[].line else error("not an array") end' "$matrix" 2> /dev/null) \
    || fail "the line matrix is not a JSON array"
  [ -n "$lines" ] || fail "no release line was discovered"
  run_url="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-cozystack/cozystack}/actions/runs/${GITHUB_RUN_ID:-}"
  printf '## Release line baseline\n\n'
  while IFS= read -r line; do
    sha=$(jq -r --arg l "$line" '.[] | select(.line == $l) | .sha' "$matrix")
    state=$(jq -r --arg l "$line" '.[] | select(.line == $l) | .state' "$matrix")
    unit=$(field "$results/unit-$line.env" result)
    e2e=$(field "$results/e2e-$line.env" result)
    tests=$(field "$results/e2e-$line.env" tests)
    digest=$(field "$results/e2e-$line.env" digest)
    rebuilt=$(field "$results/e2e-$line.env" rebuilt)
    verdict=green
    [ "$unit" = success ] || verdict=red
    if [ "$e2e" != success ] || ! [[ ${tests:-0} =~ ^[1-9][0-9]*$ ]]; then
      verdict=red
    fi
    [ "$verdict" = green ] || red=1
    printf '### %s: %s\n\n' "$line" "$verdict"
    # shellcheck disable=SC2016 # markdown code spans
    printf -- '- pinned SHA: `%s`\n' "$sha"
    printf -- '- artifact: %s, rebuilt: %s (found %s at discovery)\n' \
      "${digest:+\`cozystack-packages@$digest\`}${digest:-none tested}" "${rebuilt:-unknown}" "$state"
    printf -- '- unit: %s\n' "${unit:-no result}"
    printf -- '- e2e: %s, %s test cases\n' "${e2e:-no result}" "${tests:-no}"
    printf -- '- run: %s\n\n' "$run_url"
  done <<< "$lines"
  return "$red"
}

[ $# -ge 1 ] || die "usage: release-baseline.sh <command> [arguments]; see the header"
command=$1
shift
case "$command" in
  discover) cmd_discover "$@" ;;
  artifact) cmd_artifact "$@" ;;
  verify) cmd_verify "$@" ;;
  stage) cmd_stage "$@" ;;
  unit-commands) cmd_unit_commands "$@" ;;
  toolchain) cmd_toolchain "$@" ;;
  record) cmd_record "$@" ;;
  report) cmd_report "$@" ;;
  *) die "unknown command '$command'" ;;
esac
