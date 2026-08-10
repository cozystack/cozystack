#!/usr/bin/env bats
# EXIT-TRAP DEBT: 9 -- see hack/bats-no-exit-trap.bats; lower it as the traps go, delete it at zero.
# EXECUTES the release-changelog logic instead of asserting about its YAML.
#
# Its companion, hack/release-changelog-contract.bats, greps the workflow files.
# That catches a rename or a deleted `if:`, and nothing else: it cannot tell
# whether the shell inside a `run:` block does what the step name claims, and it
# stays green while the logic underneath is wrong. Everything here runs for real
# against throwaway git repositories and asserts on outcomes.
#
# What is genuinely covered:
#
#   * hack/changelog-preserve.sh against real remotes — a branch that exists, one
#     that does not, one with no changelog, one with an empty changelog, and the
#     full force-push cycle promote actually performs.
#   * hack/validate-changelog.sh against every current-era changelog in the tree
#     plus hand-built truncated / wrong-version / whitespace inputs.
#   * finalize's release-body selection, executed under node with the same inputs
#     the workflow gives it.
#   * The GitHub Actions `needs` + `if:` semantics the whole non-blocking design
#     rests on, run under `act` when Docker is available (see
#     hack/testdata/needs-semantics.yaml).
#
# Still NOT covered, and worth saying plainly:
#
#   * The AI generation step. Unobservable without Copilot and a paid run.
#   * The real GitHub API calls — gh pr create/edit, updateRelease. Only their
#     surrounding shell is exercised.
#   * That the sparse tooling checkout resolves on GitHub's runners. actions/
#     checkout is not runnable here; the contract suite pins its presence.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
PRESERVE="$REPO_ROOT/hack/changelog-preserve.sh"
VALIDATE="$REPO_ROOT/hack/validate-changelog.sh"
PARSE_RC="$REPO_ROOT/hack/parse-rc-tag.sh"
SELECT="$REPO_ROOT/hack/select-changelog-source.sh"
PROMOTE="$REPO_ROOT/.github/workflows/promote-rc.yaml"
FINALIZE="$REPO_ROOT/.github/workflows/pull-requests-release.yaml"

# Body of a top-level workflow job, so an assertion about one job cannot be
# satisfied — or broken — by an unrelated one. Same shape as the contract suite's.
job_block() {
  awk -v job="  $1:" '
    $0 == job { inside = 1; next }
    /^  [a-z0-9_-]+:$/ { inside = 0 }
    inside' "$2"
}

# One named step, for pins about a specific script rather than about a job. The
# parse job is ~180 lines, most of them the e2e gate, so a job-scoped negative
# pin still fires on string handling that has nothing to do with tag policy.
step_block() {
  awk -v name="      - name: $1" '
    $0 == name { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside' "$2"
}

# Executable lines only: a YAML comment mentioning the pinned pattern must not
# satisfy — or break — a pin. Same helper the contract suite uses.
code_lines() {
  local rc=0
  grep -v '^[[:space:]]*#' || rc=$?
  [ "$rc" -le 1 ]
}

# A bare "remote" plus a working clone, wired the way promote-rc.yaml sees them.
# Everything lives under one temp dir the caller removes.
make_fixture() {
  root="$1"
  mkdir -p "$root/remote" "$root/work"
  git init -q --bare "$root/remote"
  git init -q "$root/work"
  (
    cd "$root/work"
    git config user.email ci@example.invalid
    git config user.name  CI
    git config commit.gpgsign false
    git remote add origin "$root/remote"
    mkdir -p docs/changelogs
    echo "seed" > README.md
    git add -A
    git commit -qm "seed"
    git push -q origin HEAD:refs/heads/main
  )
}

# A changelog that hack/validate-changelog.sh accepts, so fixtures exercise the
# preserve path rather than tripping validation for unrelated reasons.
write_changelog() {
  dest="$1"; version="$2"
  mkdir -p "$(dirname "$dest")"
  cat > "$dest" <<EOF
<!--
https://github.com/cozystack/cozystack/releases/tag/v${version}
-->

# v${version} (2026-07-22)

Fixture changelog.

## Fixes

* **Something**: description ([**@someone**](https://github.com/someone) in #1).

**Full Changelog**: https://github.com/cozystack/cozystack/compare/v1.0.0...v${version}
EOF
}

# A changelog that hack/validate-changelog.sh REJECTS: header + release-link
# comment but no '## ' section and no compare link — the realistic shape of a
# truncated AI run or a botched hand-commit.
write_invalid_changelog() {
  dest="$1"; version="$2"
  mkdir -p "$(dirname "$dest")"
  cat > "$dest" <<EOF
<!--
https://github.com/cozystack/cozystack/releases/tag/v${version}
-->

# v${version} (2026-07-24)

Truncated fixture — no sections, no compare link.
EOF
}

# The rc-tag parser is shared by changelog-rc.yaml and mirrored inline by the
# reusable changelog-generate.yaml. It must REJECT whitespace outright, not strip
# it: a stripped 'v1. 6.0-rc.2' silently becomes a different tag, and a caller
# keying its concurrency lane on the raw input would race two spellings of the
# same tag to push the same staging branch. Execute it on the adversarial inputs
# from the review directly.
@test "parse-rc-tag: accepts a canonical rc tag and rejects whitespace / malformed ones" {
  [ -x "$PARSE_RC" ] || { echo "hack/parse-rc-tag.sh missing or not executable" >&2; exit 1; }

  # Canonical tag: exactly the three reconstructed key=value lines.
  out="$("$PARSE_RC" v1.6.0-rc.2)" || { echo "parse-rc-tag rejected a valid rc tag" >&2; exit 1; }
  want="rc_tag=v1.6.0-rc.2
stable_version=1.6.0
rc_branch=release-1.6.0-rc.2"
  [ "$out" = "$want" ] || {
    echo "parse-rc-tag output is wrong for v1.6.0-rc.2." >&2
    echo "want:" >&2; printf '%s\n' "$want" | sed 's/^/  /' >&2
    echo "got:"  >&2; printf '%s\n' "$out"  | sed 's/^/  /' >&2
    exit 1
  }

  # Every adversarial input must be REJECTED (exit non-zero) AND write nothing to
  # stdout, so a caller's `>> "$GITHUB_OUTPUT"` never gets a stray line from a
  # partially-accepted parse.
  tab="$(printf '\t')"
  for bad in \
    'v1. 6.0-rc.2' \
    ' v1.6.0-rc.2' \
    'v1.6.0-rc.2 ' \
    "v1.6.0-rc.2${tab}" \
    'v1.6.0' \
    'v1.6.0-rc' \
    '1.6.0-rc.2' \
    'v1.6.0-rc.2x' \
    ''; do
    if o="$("$PARSE_RC" "$bad" 2>/dev/null)"; then
      echo "parse-rc-tag ACCEPTED an invalid input: '$bad'" >&2
      exit 1
    fi
    [ -z "$o" ] || {
      echo "parse-rc-tag rejected '$bad' but still wrote to stdout: '$o'" >&2
      exit 1
    }
  done
}

@test "preserve: rescues a hand-written changelog from an existing staging branch" {
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  make_fixture "$tmp"

  # A maintainer commits a changelog onto release-1.6.0 — the documented recovery
  # when generation fails.
  (
    cd "$tmp/work"
    git checkout -qb release-1.6.0
    write_changelog docs/changelogs/v1.6.0.md 1.6.0
    echo "HAND-WRITTEN MARKER" >> docs/changelogs/v1.6.0.md
    git add -A && git commit -qm "hand-written changelog"
    git push -q origin HEAD:refs/heads/release-1.6.0
    git checkout -q main
    rm -f docs/changelogs/v1.6.0.md
  )

  out="$tmp/preserved.md"
  ( cd "$tmp/work" && "$PRESERVE" release-1.6.0 1.6.0 "$out" ) || {
    echo "preserve reported nothing to preserve, but the branch has a changelog" >&2
    exit 1
  }
  [ -s "$out" ] || { echo "preserve exited 0 but wrote no file" >&2; exit 1; }
  grep -q 'HAND-WRITTEN MARKER' "$out" || {
    echo "preserved file is not the maintainer's version" >&2; exit 1; }
}

@test "preserve: survives promote's full force-push rebuild cycle" {
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  make_fixture "$tmp"

  (
    cd "$tmp/work"
    git checkout -qb release-1.6.0
    write_changelog docs/changelogs/v1.6.0.md 1.6.0
    echo "HAND-WRITTEN MARKER" >> docs/changelogs/v1.6.0.md
    git add -A && git commit -qm "hand-written changelog"
    git push -q origin HEAD:refs/heads/release-1.6.0
    git checkout -q main
    rm -f docs/changelogs/v1.6.0.md
  )

  # Replay the exact sequence from `Prepare stable branch`: preserve, reset the
  # branch onto the rc tree (-B), restore, commit, force-push.
  (
    cd "$tmp/work"
    PRESERVED="$(mktemp)"
    if "$PRESERVE" release-1.6.0 1.6.0 "$PRESERVED"; then :; else PRESERVED=""; fi
    git checkout -qB release-1.6.0 main
    if [ -n "$PRESERVED" ]; then
      mkdir -p docs/changelogs
      cp "$PRESERVED" docs/changelogs/v1.6.0.md
    fi
    git add -A
    git commit -qm "Prepare release v1.6.0" || true
    git push -qf origin HEAD:refs/heads/release-1.6.0
  )

  # The maintainer's file must still be on the rebuilt remote branch.
  content="$( cd "$tmp/work" && git fetch -q origin release-1.6.0 && git show FETCH_HEAD:docs/changelogs/v1.6.0.md )"
  printf '%s\n' "$content" | grep -q 'HAND-WRITTEN MARKER' || {
    echo "The force-push rebuild destroyed the hand-written changelog — the exact" >&2
    echo "regression the preserve step exists to prevent." >&2
    exit 1
  }
}

@test "preserve: reports nothing to preserve for absent branch, absent file, empty file" {
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  make_fixture "$tmp"

  # No such branch — the ordinary first-promotion case.
  ( cd "$tmp/work" && "$PRESERVE" release-9.9.9 9.9.9 "$tmp/a.md" ) && {
    echo "preserve claimed success for a branch that does not exist" >&2; exit 1; }
  [ ! -f "$tmp/a.md" ] || { echo "preserve wrote a file for a missing branch" >&2; exit 1; }

  # Branch exists, carries no changelog.
  (
    cd "$tmp/work"
    git checkout -qb release-1.7.0
    echo x > other.txt && git add -A && git commit -qm "no changelog"
    git push -q origin HEAD:refs/heads/release-1.7.0
    git checkout -q main
  )
  ( cd "$tmp/work" && "$PRESERVE" release-1.7.0 1.7.0 "$tmp/b.md" ) && {
    echo "preserve claimed success for a branch with no changelog" >&2; exit 1; }

  # Branch exists, changelog is empty — must NOT be preserved, or it would
  # suppress generation while being useless as release notes.
  (
    cd "$tmp/work"
    git checkout -q release-1.7.0
    mkdir -p docs/changelogs && : > docs/changelogs/v1.7.0.md
    git add -A && git commit -qm "empty changelog"
    git push -qf origin HEAD:refs/heads/release-1.7.0
    git checkout -q main
  )
  ( cd "$tmp/work" && "$PRESERVE" release-1.7.0 1.7.0 "$tmp/c.md" ) && {
    echo "preserve carried over an EMPTY changelog; it would suppress generation" >&2
    echo "and then publish as blank release notes." >&2
    exit 1
  }
  [ ! -f "$tmp/c.md" ] || { echo "preserve left an empty artefact behind" >&2; exit 1; }
  return 0
}

@test "pickup: changelog-preserve.sh fetches an rc-time changelog from the rc staging branch" {
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  make_fixture "$tmp"

  # changelog-rc.yaml committed the changelog onto the rc staging branch
  # release-1.6.0-rc.2 at rc time (docs/changelogs/vX.Y.Z.md — the stable name).
  (
    cd "$tmp/work"
    git checkout -qb release-1.6.0-rc.2
    write_changelog docs/changelogs/v1.6.0.md 1.6.0
    echo "RC-TIME MARKER" >> docs/changelogs/v1.6.0.md
    git add -A && git commit -qm "docs: add changelog for v1.6.0"
    git push -q origin HEAD:refs/heads/release-1.6.0-rc.2
    git checkout -q main
    rm -f docs/changelogs/v1.6.0.md
  )

  # The generation core (changelog-generate.yaml) does exactly this to COPY the
  # rc-time changelog into the deliverable path instead of regenerating it — it
  # runs `mkdir -p docs/changelogs` first (git prunes the emptied dir on the
  # `checkout main` above), so mirror that.
  out="$tmp/work/docs/changelogs/v1.6.0.md"
  ( cd "$tmp/work" && mkdir -p docs/changelogs && "$PRESERVE" release-1.6.0-rc.2 1.6.0 docs/changelogs/v1.6.0.md ) || {
    echo "pickup reported nothing to fetch, but the rc staging branch has a changelog" >&2
    exit 1
  }
  [ -s "$out" ] || { echo "pickup exited 0 but wrote no file" >&2; exit 1; }
  grep -q 'RC-TIME MARKER' "$out" || {
    echo "picked-up file is not the rc-time changelog" >&2; exit 1; }
  # And it must be publishable — the generation core validates it next.
  "$VALIDATE" "$out" 1.6.0 >/dev/null 2>&1 || {
    echo "picked-up changelog does not pass the validator" >&2; exit 1; }
}

# hack/select-changelog-source.sh is the validate-then-fall-through the reusable
# core delegates to. Execute it for real against the three cases that matter, so a
# regression (e.g. dropping a validation, or letting an invalid source win) is
# caught by outcome rather than by a grep that a dead block could satisfy.
@test "select-source: a valid rc-tag-tree changelog wins over the staging branch" {
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  make_fixture "$tmp"

  # A DIFFERENT valid changelog on the rc staging branch, to prove the tag tree wins.
  (
    cd "$tmp/work"
    git checkout -qb release-1.6.0-rc.2
    write_changelog docs/changelogs/v1.6.0.md 1.6.0
    echo "BRANCH MARKER" >> docs/changelogs/v1.6.0.md
    git add -A && git commit -qm "branch changelog"
    git push -q origin HEAD:refs/heads/release-1.6.0-rc.2
    git checkout -q main
    rm -f docs/changelogs/v1.6.0.md
  )
  # A valid changelog in the working tree (the rc tag tree).
  ( cd "$tmp/work" && write_changelog docs/changelogs/v1.6.0.md 1.6.0 && echo "TAG-TREE MARKER" >> docs/changelogs/v1.6.0.md )

  out="$( cd "$tmp/work" && "$SELECT" 1.6.0 release-1.6.0-rc.2 )" || { echo "select-source failed" >&2; exit 1; }
  src="$(printf '%s\n' "$out" | sed -n 's/^source=//p' | tail -n1)"
  [ "$src" = rc-tag ] || {
    echo "expected source=rc-tag; got '$src'. Full output:" >&2; printf '%s\n' "$out" | sed 's/^/  /' >&2; exit 1; }
  grep -q 'TAG-TREE MARKER' "$tmp/work/docs/changelogs/v1.6.0.md" || {
    echo "the rc-tag-tree changelog was not the one selected" >&2; exit 1; }
}

@test "select-source: an invalid rc-tag-tree changelog falls through to a valid staging branch" {
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  make_fixture "$tmp"

  # A VALID changelog on the rc staging branch.
  (
    cd "$tmp/work"
    git checkout -qb release-1.6.0-rc.2
    write_changelog docs/changelogs/v1.6.0.md 1.6.0
    echo "BRANCH MARKER" >> docs/changelogs/v1.6.0.md
    git add -A && git commit -qm "branch changelog"
    git push -q origin HEAD:refs/heads/release-1.6.0-rc.2
    git checkout -q main
    rm -f docs/changelogs/v1.6.0.md
  )
  # An INVALID changelog in the working tree — must be discarded, not win.
  ( cd "$tmp/work" && write_invalid_changelog docs/changelogs/v1.6.0.md 1.6.0 )

  out="$( cd "$tmp/work" && "$SELECT" 1.6.0 release-1.6.0-rc.2 )" || { echo "select-source failed" >&2; exit 1; }
  src="$(printf '%s\n' "$out" | sed -n 's/^source=//p' | tail -n1)"
  [ "$src" = rc-branch ] || {
    echo "expected source=rc-branch (invalid tag tree must fall through); got '$src'." >&2
    printf '%s\n' "$out" | sed 's/^/  /' >&2; exit 1; }
  printf '%s\n' "$out" | grep -q '::warning::' || {
    echo "no loud ::warning:: for the discarded invalid tag-tree changelog" >&2; exit 1; }
  grep -q 'BRANCH MARKER' "$tmp/work/docs/changelogs/v1.6.0.md" || {
    echo "the valid rc-staging-branch changelog was not copied into place" >&2; exit 1; }
}

@test "select-source: invalid in both the tag tree and the staging branch falls through to generate" {
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  make_fixture "$tmp"

  # An INVALID changelog on the rc staging branch.
  (
    cd "$tmp/work"
    git checkout -qb release-1.6.0-rc.2
    write_invalid_changelog docs/changelogs/v1.6.0.md 1.6.0
    git add -A && git commit -qm "invalid branch changelog"
    git push -q origin HEAD:refs/heads/release-1.6.0-rc.2
    git checkout -q main
    rm -f docs/changelogs/v1.6.0.md
  )
  # An INVALID changelog in the working tree too.
  ( cd "$tmp/work" && write_invalid_changelog docs/changelogs/v1.6.0.md 1.6.0 )

  out="$( cd "$tmp/work" && "$SELECT" 1.6.0 release-1.6.0-rc.2 )" || { echo "select-source failed" >&2; exit 1; }
  src="$(printf '%s\n' "$out" | sed -n 's/^source=//p' | tail -n1)"
  [ "$src" = none ] || {
    echo "expected source=none (both invalid -> generate); got '$src'." >&2
    printf '%s\n' "$out" | sed 's/^/  /' >&2; exit 1; }
  # Both discards must be reported loudly.
  [ "$(printf '%s\n' "$out" | grep -c '::warning::')" -eq 2 ] || {
    echo "expected exactly two ::warning:: lines (one per discarded invalid source)." >&2
    printf '%s\n' "$out" | sed 's/^/  /' >&2; exit 1; }
  # The invalid file must not be left behind to poison a later step.
  [ ! -f "$tmp/work/docs/changelogs/v1.6.0.md" ] || {
    echo "an invalid changelog was left on disk instead of being discarded" >&2; exit 1; }
}

# promote-rc.yaml's rc-tag parse is github-script (JS), so mirror its exact policy
# under node and run the adversarial inputs from the review through it: whitespace
# is REJECTED, never trimmed-and-accepted. The greps keep the mirror honest.
@test "promote-rc parse: rejects whitespace-bearing rc tags (mirrors the workflow's github-script)" {
  command -v node >/dev/null || { echo "node unavailable; skipping"; return 0; }
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  cat > "$tmp/parse.js" <<'JS'
function parse(rc) {
  if (/\s/.test(rc)) return 'reject:whitespace';
  const m = rc.match(/^v(\d+)\.(\d+)\.(\d+)-rc\.(\d+)$/);
  if (!m) return 'reject:format';
  return 'accept:' + m[1] + '.' + m[2] + '.' + m[3];
}
const lines = process.argv.slice(2).map((a) => a + '=' + parse(a));
require('fs').writeFileSync(1, lines.join('\n') + '\n');
JS

  tab="$(printf '\t')"
  node "$tmp/parse.js" \
    'v1.6.0-rc.2' \
    ' v1.6.0-rc.2' \
    'v1.6.0-rc.2 ' \
    "v1.6.0-rc.2${tab}" \
    'v1. 6.0-rc.2' \
    'v1.6.0' > "$tmp/out.txt" 2>"$tmp/err.txt" || { echo "node failed:" >&2; cat "$tmp/err.txt" >&2; exit 1; }

  want="v1.6.0-rc.2=accept:1.6.0
 v1.6.0-rc.2=reject:whitespace
v1.6.0-rc.2 =reject:whitespace
v1.6.0-rc.2${tab}=reject:whitespace
v1. 6.0-rc.2=reject:whitespace
v1.6.0=reject:format"
  got="$(cat "$tmp/out.txt")"
  [ "$got" = "$want" ] || {
    echo "promote parse policy mismatch." >&2
    echo "want:" >&2; printf '%s\n' "$want" | sed 's/^/  /' >&2
    echo "got:"  >&2; printf '%s\n' "$got"  | sed 's/^/  /' >&2
    exit 1
  }

  # The mirror must match what promote-rc.yaml actually does: reject whitespace,
  # and NOT trim-and-accept. Scoped to the parse STEP, not the parse job: the job
  # also holds the e2e gate's github-script, whose own string handling would
  # otherwise trip a negative pin about tag policy.
  parse_step="$(step_block 'Parse and validate rc tag' "$PROMOTE")"
  [ -n "$parse_step" ] || { echo "promote-rc.yaml has no 'Parse and validate rc tag' step." >&2; exit 1; }
  printf '%s\n' "$parse_step" | code_lines | grep -qF '/\s/.test(rc)' || {
    echo "promote-rc.yaml's parse no longer rejects whitespace with /\\s/.test(rc)." >&2; exit 1; }
  printf '%s\n' "$parse_step" | code_lines | grep -qF '.trim()' && {
    echo "promote-rc.yaml's parse still trims the rc tag; policy diverges from the changelog job." >&2; exit 1; }
  return 0
}

@test "finalize: the release-body selection runs and picks the changelog" {
  command -v node >/dev/null || { echo "node unavailable; skipping"; return 0; }

  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  # The same decision finalize makes, extracted verbatim in shape: read the file,
  # refuse whitespace-only, otherwise use it as the body.
  #
  # All three cases run in ONE node invocation. Driving them from a shell loop
  # produced an empty capture for the missing-file case under this repo's
  # cozytest runner — reproducibly, with node exiting 0 and writing nothing, and
  # not reproducible outside it. Rather than assert around a runner quirk (which
  # would leave the assertion quietly meaningless), the decision table is
  # computed in one process and compared as a whole.
  cat > "$tmp/body.js" <<'JS'
const fs = require('fs');

// Verbatim in shape from pull-requests-release.yaml's Publish draft release step.
function selectBody(changelogPath) {
  let body;
  let decision;
  if (fs.existsSync(changelogPath)) {
    const raw = fs.readFileSync(changelogPath, 'utf8');
    if (raw.trim().length === 0) {
      decision = 'refuse-whitespace';
    } else {
      body = raw;
      decision = 'use-file';
    }
  } else {
    decision = 'absent';
  }
  const payload = { draft: false, ...(body ? { body } : {}) };
  return decision + ':' + Object.prototype.hasOwnProperty.call(payload, 'body');
}

const lines = process.argv.slice(2).map((p) => {
  const label = p.replace(/^.*\//, '');
  return label + '=' + selectBody(p);
});
fs.writeFileSync(1, lines.join('\n') + '\n');
JS

  write_changelog "$tmp/good.md" 1.6.0
  printf '   \n\n \n' > "$tmp/ws.md"

  node "$tmp/body.js" "$tmp/good.md" "$tmp/ws.md" "$tmp/nope.md" > "$tmp/out.txt" 2>"$tmp/err.txt" || {
    echo "node failed:" >&2; cat "$tmp/err.txt" >&2; exit 1; }

  want="good.md=use-file:true
ws.md=refuse-whitespace:false
nope.md=absent:false"
  got="$(cat "$tmp/out.txt")"

  [ "$got" = "$want" ] || {
    echo "finalize's release-body decision table is wrong." >&2
    echo "expected:" >&2; printf '%s\n' "$want" | sed 's/^/  /' >&2
    echo "got:" >&2;      printf '%s\n' "$got"  | sed 's/^/  /' >&2
    echo "A whitespace-only or absent changelog reaching the body would publish" >&2
    echo "blank release notes instead of leaving the draft's text in place." >&2
    exit 1
  }

  # And the shape asserted here must be the shape finalize actually uses.
  grep -q 'raw.trim().length === 0' "$FINALIZE" || {
    echo "finalize's whitespace guard changed shape; this test now proves nothing" >&2
    echo "about the real workflow." >&2
    exit 1
  }
}

# The one assumption no amount of YAML reading can settle: that GitHub really
# runs a job whose `needs` includes a FAILED job, when its `if:` gates only on a
# different, successful one. The entire non-blocking design rests on it. `act`
# executes the fixture workflow for real and the assertions below read its output.
#
# Docker-gated: no Docker, no verdict — and the test says so rather than passing
# quietly, because a silent skip here is indistinguishable from a green result on
# the one thing that most needs proving.
@test "act: open-pr runs when the changelog job fails, and failed-job outputs are empty" {
  if ! command -v act >/dev/null || ! docker info >/dev/null 2>&1; then
    echo "act or Docker unavailable — GitHub needs/if semantics NOT verified in this run." >&2
    return 0
  fi

  fixture="$REPO_ROOT/hack/testdata/needs-semantics.yaml"
  [ -f "$fixture" ] || { echo "missing fixture $fixture" >&2; exit 1; }

  image="debian:stable-slim"
  docker image inspect "$image" >/dev/null 2>&1 || {
    echo "$image not present locally and pulling is disabled — semantics NOT verified." >&2
    return 0
  }

  out="$(cd "$REPO_ROOT" && act push -W "$fixture" -P "ubuntu-latest=$image" --pull=false 2>&1 || true)"

  printf '%s\n' "$out" | grep -q 'OPEN_PR_RAN=yes' || {
    echo "open-pr did NOT run after the changelog job failed. The non-blocking" >&2
    echo "guarantee does not hold: a Copilot or npm outage would leave a pushed" >&2
    echo "staging branch and a drafted release with no promote PR." >&2
    printf '%s\n' "$out" | tail -40 >&2
    exit 1
  }

  printf '%s\n' "$out" | grep -q 'changelog_result=failure' || {
    echo "The fixture's changelog job did not actually fail; the test proved nothing." >&2
    exit 1
  }

  # Outputs of a failed job come back empty. This is WHY open-pr re-establishes
  # changelog presence from disk instead of trusting needs.changelog.outputs.
  printf '%s\n' "$out" | grep -q 'changelog_present=\[\]' || {
    echo "A failed job's outputs were expected to be empty. If they now propagate," >&2
    echo "the on-disk re-check in open-pr may be masking a simpler design." >&2
    exit 1
  }

  # And a successful job's outputs must still reach the dependent job.
  printf '%s\n' "$out" | grep -q 'stable_version=\[1.6.0\]' || {
    echo "promote's outputs did not reach open-pr; open-pr could not target the" >&2
    echo "right branch or version." >&2
    exit 1
  }
}
