#!/usr/bin/env bats
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
#   * The github-script steps that set a release body, finalize's and
#     update-releasenotes', taken verbatim from the workflows and run under node
#     against fixture trees.
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
#
# Test-level EXIT traps replace Bats' own handler and hide failing TAP results.
# Cleanup follows aborting assertions; see docs/agents/e2e-testing.md.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
PRESERVE="$REPO_ROOT/hack/changelog-preserve.sh"
VALIDATE="$REPO_ROOT/hack/validate-changelog.sh"
PARSE_RC="$REPO_ROOT/hack/parse-rc-tag.sh"
SELECT="$REPO_ROOT/hack/select-changelog-source.sh"
PROMOTE="$REPO_ROOT/.github/workflows/promote-rc.yaml"
FINALIZE="$REPO_ROOT/.github/workflows/pull-requests-release.yaml"
SYNC="$REPO_ROOT/.github/workflows/update-releasenotes.yaml"

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

# Exactly <count> copies of <string> and no newline; awk passes the bytes of a
# multi-byte <string> through untouched.
repeat() {
  awk -v s="$1" -v n="$2" 'BEGIN { for (i = 0; i < n; i++) printf "%s", s }'
}

# node runs the workflows' github-script, so it is required, not optional: a
# test that passes without it has run nothing. The unit lane's runner has it,
# which hack/pr-labeler-contract.bats already depends on.
need_node() {
  command -v node >/dev/null 2>&1 && return 0
  echo "node is required to run the workflows' github-script steps" >&2
  return 1
}

# Runs one github-script step under node with its script exactly as the
# workflow carries it, from <tree> as the working directory; github and core
# are stand-ins. Leaves the step's log in <out>/log, one line per call to GitHub
# or core in <out>/calls (ending in `ok`, or `threw` if the step threw), and
# each body sent with updateRelease in <out>/body.<release_id>.
#
#   run_step <workflow> <job> <step> <tree> <releases.json> <out> [<tag>]
run_step() {
  mkdir -p "$6"
  JOB="$2" STEP="$3" yq '.jobs[strenv(JOB)].steps[] | select(.name == strenv(STEP)) | .with.script' "$1" > "$6/script.js"
  [ -s "$6/script.js" ] || { echo "no script for step '$3' in $1" >&2; return 1; }
  cat > "$6/harness.js" <<'JS'
const fs = require('fs');
const path = require('path');
const [scriptFile, releasesFile, outDir] = process.argv.slice(2);
let script = fs.readFileSync(scriptFile, 'utf8');
if (process.env.TAG) script = script.split('${{ steps.get_tag.outputs.tag }}').join(process.env.TAG);
if (script.includes('${{')) throw new Error('an expression in the script was left unsubstituted');
const releases = JSON.parse(fs.readFileSync(releasesFile, 'utf8'));
const calls = [];
const github = { rest: { repos: {
  listReleases: async () => ({ data: releases }),
  updateRelease: async (p) => {
    const keys = Object.keys(p).filter((k) => !['owner', 'repo', 'release_id', 'body'].includes(k)).sort();
    calls.push(['updateRelease', p.release_id, ...keys.map((k) => `${k}=${p[k]}`), ...('body' in p ? ['body'] : [])].join(' '));
    if ('body' in p) fs.writeFileSync(path.join(outDir, `body.${p.release_id}`), p.body);
    return { data: { ...releases.find((r) => r.id === p.release_id), ...p } };
  },
} } };
const context = { repo: { owner: 'cozystack', repo: 'cozystack' } };
const core = {
  setOutput: (k, v) => calls.push(`setOutput ${k}=${v}`),
  setFailed: (m) => calls.push(`setFailed ${m}`),
};
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
new AsyncFunction('require', 'github', 'context', 'core', script)(require, github, context, core)
  .then(() => 'ok', (e) => `threw ${e.message}`)
  .then((result) => fs.writeFileSync(path.join(outDir, 'calls'), calls.concat(result).join('\n') + '\n'));
JS
  ( cd "$4" && TAG="${7:-}" node "$6/harness.js" "$6/script.js" "$5" "$6" > "$6/log" 2>&1 ) || {
    echo "node failed for step '$3':" >&2; sed 's/^/    /' "$6/log" >&2; return 1; }
  [ -s "$6/calls" ] || { echo "step '$3' recorded nothing" >&2; return 1; }
}

# The body finalize publishes for v1.7.0 in place of one over 100000 bytes.
LINK_V170='The release notes for v1.7.0 are too long for a GitHub release description; the full changelog is [docs/changelogs/v1.7.0.md](https://github.com/cozystack/cozystack/blob/v1.7.0/docs/changelogs/v1.7.0.md).'

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
  rm -rf "$tmp"
}

@test "preserve: survives promote's full force-push rebuild cycle" {
  tmp="$(mktemp -d)"
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
  rm -rf "$tmp"
}

@test "preserve: reports nothing to preserve for absent branch, absent file, empty file" {
  tmp="$(mktemp -d)"
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
  rm -rf "$tmp"
  return 0
}

@test "pickup: changelog-preserve.sh fetches an rc-time changelog from the rc staging branch" {
  tmp="$(mktemp -d)"
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
  rm -rf "$tmp"
}

# hack/select-changelog-source.sh is the validate-then-fall-through the reusable
# core delegates to. Execute it for real against the three cases that matter, so a
# regression (e.g. dropping a validation, or letting an invalid source win) is
# caught by outcome rather than by a grep that a dead block could satisfy.
@test "select-source: a valid rc-tag-tree changelog wins over the staging branch" {
  tmp="$(mktemp -d)"
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
  rm -rf "$tmp"
}

@test "select-source: an invalid rc-tag-tree changelog falls through to a valid staging branch" {
  tmp="$(mktemp -d)"
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
  rm -rf "$tmp"
}

@test "select-source: invalid in both the tag tree and the staging branch falls through to generate" {
  tmp="$(mktemp -d)"
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
  rm -rf "$tmp"
}

# promote-rc.yaml's rc-tag parse is github-script (JS), so mirror its exact policy
# under node and run the adversarial inputs from the review through it: whitespace
# is REJECTED, never trimmed-and-accepted. The greps keep the mirror honest.
@test "promote-rc parse: rejects whitespace-bearing rc tags (mirrors the workflow's github-script)" {
  command -v node >/dev/null || { echo "node unavailable; skipping"; return 0; }
  tmp="$(mktemp -d)"

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
  rm -rf "$tmp"
  return 0
}

# finalize's Publish draft release step, run as the workflow carries it, one
# merge-commit tree per case. Whatever the tree holds, the release is
# published: with the release notes, else the changelog, else the draft's own
# body, and with a link in place of anything over 100000 bytes, since the tags
# already exist and a body GitHub refuses would strand the release.
@test "finalize: publishes the release notes, else the changelog, and a link over 100000 bytes" {
  need_node
  tmp="$(mktemp -d)"
  cat > "$tmp/releases.json" <<'EOF'
[
  {"id": 1, "draft": true, "prerelease": false, "tag_name": "v1.7.0", "name": "v1.7.0", "body": "Promoted from v1.7.0-rc.3."},
  {"id": 2, "draft": false, "prerelease": false, "tag_name": "v1.6.5", "name": "v1.6.5", "body": "v1.6.5"}
]
EOF
  notes=docs/releasenotes/v1.7.0.md
  cl=docs/changelogs/v1.7.0.md
  for c in notes blanknotes changelog neither blank at over multibyte overnotes; do
    mkdir -p "$tmp/$c/docs/releasenotes" "$tmp/$c/docs/changelogs"
  done
  printf '# Cozystack v1.7.0\n\nThe highlights.\n' > "$tmp/notes/$notes"
  printf ' \n\t\n' > "$tmp/blanknotes/$notes"
  printf '   \n\n \n' > "$tmp/blank/$cl"
  for c in notes blanknotes changelog; do write_changelog "$tmp/$c/$cl" 1.7.0; done
  repeat a 100000 > "$tmp/at/$cl"
  repeat a 100001 > "$tmp/over/$cl"
  # 40000 characters, 120000 bytes: the limit is in bytes.
  repeat '€' 40000 > "$tmp/multibyte/$cl"
  repeat a 100001 > "$tmp/overnotes/$notes"
  write_changelog "$tmp/overnotes/$cl" 1.7.0

  for spec in notes:$notes blanknotes:$cl changelog:$cl at:$cl over:link multibyte:link overnotes:link neither:none blank:none; do
    c="${spec%%:*}"; want="${spec#*:}"
    run_step "$FINALIZE" finalize 'Publish draft release' "$tmp/$c" "$tmp/releases.json" "$tmp/out.$c" v1.7.0
    with_body=' body'
    [ "$want" != none ] || with_body=''
    expected="$(printf 'ok\nsetOutput make_latest=true\nupdateRelease 1 draft=false make_latest=true prerelease=false%s\n' "$with_body")"
    [ "$(sort "$tmp/out.$c/calls")" = "$expected" ] || {
      echo "$c: finalize did something other than publishing v1.7.0:" >&2
      sed 's/^/  /' "$tmp/out.$c/calls" "$tmp/out.$c/log" >&2; exit 1; }
    case "$want" in
      none)
        grep -q '^::warning::' "$tmp/out.$c/log" || {
          echo "$c: the draft's body was kept without a ::warning::" >&2; exit 1; } ;;
      link)
        [ "$(cat "$tmp/out.$c/body.1")" = "$LINK_V170" ] || {
          echo "$c: the body over 100000 bytes was not replaced by the link:" >&2
          head -c 300 "$tmp/out.$c/body.1" >&2; exit 1; }
        grep -q '^::warning::.* bytes, over MAX_BODY_BYTES' "$tmp/out.$c/log" || {
          echo "$c: the link was published without a ::warning::" >&2; exit 1; } ;;
      *)
        cmp -s "$tmp/out.$c/body.1" "$tmp/$c/$want" || {
          echo "$c: the body is not $want byte for byte" >&2; exit 1; }
        grep -qF "Release body from $want ($(wc -c < "$tmp/$c/$want") bytes)" "$tmp/out.$c/log" || {
          echo "$c: the log does not give the body's source and byte length:" >&2
          sed 's/^/  /' "$tmp/out.$c/log" >&2; exit 1; } ;;
    esac
  done
  rm -rf "$tmp"
}

# A release with no release notes publishes exactly what finalize published
# before they existed. Every shipped changelog is a fixture; one over 100000
# bytes, which GitHub may refuse, publishes the link instead.
@test "finalize: without release notes a shipped changelog is published byte for byte, or as the link over 100000 bytes" {
  need_node
  tmp="$(mktemp -d)"
  checked=0
  verbatim=0
  for f in "$REPO_ROOT"/docs/changelogs/v*.md; do
    [ -f "$f" ] || continue
    tag="$(basename "$f" .md)"
    mkdir -p "$tmp/$tag/docs/changelogs"
    cp "$f" "$tmp/$tag/docs/changelogs/"
    printf '[{"id": 1, "draft": true, "prerelease": false, "tag_name": "%s", "name": "%s", "body": "draft"}]\n' "$tag" "$tag" > "$tmp/$tag.json"
    run_step "$FINALIZE" finalize 'Publish draft release' "$tmp/$tag" "$tmp/$tag.json" "$tmp/out.$tag" "$tag"
    if [ "$(wc -c < "$f")" -le 100000 ]; then
      cmp -s "$tmp/out.$tag/body.1" "$f" || { echo "$tag: not published byte for byte" >&2; exit 1; }
      verbatim=$((verbatim + 1))
    else
      grep -qF "/blob/$tag/docs/changelogs/$tag.md)." "$tmp/out.$tag/body.1" || {
        echo "$tag: over 100000 bytes but not published as the link" >&2; exit 1; }
    fi
    checked=$((checked + 1))
  done
  [ "$checked" -ge 10 ] && [ "$verbatim" -ge 10 ] || {
    echo "$checked changelog(s) checked, $verbatim verbatim; the glob broke and this verifies nothing." >&2; exit 1; }
  rm -rf "$tmp"
}

# update-releasenotes' sync step, run as the workflow carries it over one main
# checkout. It makes finalize's choice of file, but a body over 100000 bytes is
# never synced, not even as the link: the tags.yaml backstop ports a changelog
# to main without its release notes, and it would replace the body finalize
# published.
@test "update-releasenotes: syncs the release notes, else the changelog, and never a body over 100000 bytes" {
  need_node
  tmp="$(mktemp -d)"
  m="$tmp/main"
  mkdir -p "$m/docs/releasenotes" "$m/docs/changelogs"
  printf '# v1.0.1\n\nThe highlights.\n' > "$m/docs/releasenotes/v1.0.1.md"
  printf ' \n' > "$m/docs/releasenotes/v1.0.2.md"
  for v in 1 2 3 8 9; do write_changelog "$m/docs/changelogs/v1.0.$v.md" "1.0.$v"; done
  repeat a 100001 > "$m/docs/changelogs/v1.0.5.md"
  repeat '€' 40000 > "$m/docs/changelogs/v1.0.6.md"
  repeat a 100000 > "$m/docs/changelogs/v1.0.7.md"
  repeat a 100001 > "$m/docs/releasenotes/v1.0.9.md"
  UP_TO_DATE="$(cat "$m/docs/changelogs/v1.0.8.md")" node -e '
    const r = (v, body) => ({ id: v, draft: false, prerelease: false, tag_name: `v1.0.${v}`, name: `v1.0.${v}`, body });
    process.stdout.write(JSON.stringify([1, 2, 3, 4, 5, 6, 7, 9].map((v) => r(v, "old body")).concat(r(8, process.env.UP_TO_DATE))));' > "$tmp/releases.json"

  run_step "$SYNC" update-releasenotes 'Update release notes from changelogs' "$m" "$tmp/releases.json" "$tmp/out"
  want="updateRelease 1 body
updateRelease 2 body
updateRelease 3 body
updateRelease 7 body
ok"
  [ "$(cat "$tmp/out/calls")" = "$want" ] || {
    echo "The sync updated other releases than v1.0.1, v1.0.2, v1.0.3 and v1.0.7:" >&2
    sed 's/^/  /' "$tmp/out/calls" "$tmp/out/log" >&2; exit 1; }
  for pair in 1:releasenotes 2:changelogs 3:changelogs 7:changelogs; do
    v="${pair%%:*}"
    cmp -s "$tmp/out/body.$v" "$m/docs/${pair#*:}/v1.0.$v.md" || {
      echo "v1.0.$v was not synced with docs/${pair#*:}/v1.0.$v.md" >&2; exit 1; }
  done
  for v in 5 6 9; do
    grep -q "^::warning::.*over MAX_BODY_BYTES.*leaving the body of release v1.0.$v as it is" "$tmp/out/log" || {
      echo "v1.0.$v, over 100000 bytes, was left alone without a ::warning:: naming it" >&2; exit 1; }
  done
  rm -rf "$tmp"
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
