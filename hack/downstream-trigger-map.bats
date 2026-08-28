#!/usr/bin/env bats
# Guards the "Downstream Repositories" trigger map in docs/agents/contributing.md.
#
# The map tells a contributor which change in this repo forces a follow-up in a
# satellite repository. Each trigger names a file on this side: hack/update-crd.sh,
# packages/core/platform/values.yaml, and so on. Rename one of those and the map
# keeps confidently pointing at a path that no longer exists, from inside the PR
# template, to every reader. The repository list is deliberately not restated here
# — it would be one more copy to drift; the map and the PR template are the two
# sources, and the tests below hold them to each other.
#
# The three tests pin, in order: every in-repo path the map cites to a file that
# exists; the workflow guarantee that Bats contracts run on every docs-only PR;
# and the map and the PR-template checklist to the same repository list, so a
# repository cannot be listed in one and forgotten in the other.
#
# WHAT THIS DOES NOT CHECK, so nobody mistakes a green tick for a correct map:
#
#   * Whether a trigger is TRUE. "Change X here and Y breaks over there" is a
#     claim about a satellite repo. Verifying it needs that repo checked out at
#     a known ref, which this suite deliberately does not do: a unit test must
#     not depend on the network or on six sibling clones. A trigger can cite a
#     path that exists here and still describe a coupling that is nonsense.
#   * Anything on the satellite side. A file named there (its Makefile, its
#     scripts/, its playbooks) is not reachable from this repo and is not
#     validated. Those names are exactly where the map is most likely to rot.
#   * Whether the map is COMPLETE. Deleting a whole trigger, or a whole block of
#     them, passes: the repository-list test compares section headings, not bodies.
#   * A path written without backticks. Extraction keys off `code spans`, which
#     is the section's convention; a lone unquoted path slips through. Stripping
#     the backticks wholesale does fail, via the anti-vacuum guard below.
#
# In short: this catches a rename on THIS side, which is the cheap half. The
# expensive half — is the coupling real, and is it still real over there — stays
# a human's job, and reviewers should not assume CI did it for them.
#
# A docs-only PR skips builds and E2E, but the `checks` job still runs the Bats
# lane. Otherwise editing this map, or another document audited by a unit
# contract, would exempt the changed input from its own guard.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
MAP_FILE="$REPO_ROOT/docs/agents/contributing.md"
TEMPLATE_FILE="$REPO_ROOT/.github/PULL_REQUEST_TEMPLATE.md"

# Body of the "## Downstream Repositories" section, up to the next h2.
map_section() {
  awk '/^## Downstream Repositories$/ { inside = 1; next }
       /^## / { inside = 0 }
       inside' "$MAP_FILE"
}

@test "every in-repo path cited by the downstream trigger map exists" {
  [ -f "$MAP_FILE" ] || { echo "missing $MAP_FILE" >&2; exit 1; }

  section="$(map_section)"
  [ -n "$section" ] || { echo "Could not locate the '## Downstream Repositories' section in $MAP_FILE" >&2; exit 1; }

  # Backticked tokens rooted at a top-level directory of THIS repo. Satellite
  # paths cited by the map (scripts/package.mk, content/en/docs/...) do not match
  # these roots and so fall out. Note this is a heuristic, not a guarantee: a
  # satellite repo has its own packages/ tree, so a backticked path from over
  # there that happens to share a root would be checked against this tree.
  # A trailing slash is prose, not a distinct path: `packages/apps/` and
  # `packages/apps` are the same anchor and would otherwise both inflate the count.
  paths="$(printf '%s\n' "$section" \
    | grep -o '`[^`]*`' \
    | tr -d '`' \
    | grep -E '^(hack|packages|cmd|api|internal|pkg|\.github)/' \
    | sed 's#/$##' \
    | sort -u)"

  # Guard against a vacuous pass: if the extraction breaks, or the section is
  # reworded so it no longer cites paths, this test must fail loudly rather than
  # silently verify nothing. The floor sits well under the real count (28 as of
  # writing) because the map legitimately shrinks too, and a red build should not
  # send the reader hunting for a bug that is really a deleted section.
  count="$(printf '%s\n' "$paths" | grep -c . || true)"
  [ "$count" -ge 20 ] || {
    echo "Extracted only $count in-repo path(s) from the trigger map; expected at least 20." >&2
    echo "Either the extraction broke and this test is verifying nothing, or the map genuinely" >&2
    echo "shrank - if a downstream repository went away, lower this floor deliberately." >&2
    exit 1
  }

  missing=""
  for path in $paths; do
    # A placeholder segment (<name>) stands for any package, so match it as a glob.
    pattern="$(printf '%s' "$path" | sed 's/<[^>]*>/*/g')"
    # Unquoted on purpose: the shell must expand the glob. No match => ls fails.
    if ! (cd "$REPO_ROOT" && ls -d $pattern) >/dev/null 2>&1; then
      missing="$missing $path"
    fi
  done

  if [ -n "$missing" ]; then
    echo "The downstream trigger map in docs/agents/contributing.md cites paths that do not exist:" >&2
    for path in $missing; do echo "  - $path" >&2; done
    echo "Fix: update the map to the new path, or drop the trigger if it no longer applies." >&2
    echo "The map is read by contributors from inside the PR template; a stale path there sends them to a dead end." >&2
    exit 1
  fi

  echo "All $count in-repo path(s) cited by the trigger map exist"
}

@test "the workflow runs Bats contracts for docs-only changes" {
  WORKFLOW="$REPO_ROOT/.github/workflows/pull-requests.yaml"
  [ -f "$WORKFLOW" ] || { echo "missing $WORKFLOW" >&2; exit 1; }

  job_if=$(yq -r '.jobs.checks.if' "$WORKFLOW")
  code_if=$(yq -r '.jobs.checks.steps[] | select(.name == "Run unit tests") | .if' "$WORKFLOW")
  docs_if=$(yq -r '.jobs.checks.steps[] | select(.name == "Run Bats unit tests for docs-only changes") | .if' "$WORKFLOW")
  docs_run=$(yq -r '.jobs.checks.steps[] | select(.name == "Run Bats unit tests for docs-only changes") | .run' "$WORKFLOW")
  controller_if=$(yq -r '.jobs.checks.steps[] | select(.name == "Run controller Go tests") | .if' "$WORKFLOW")

  [ "$job_if" = "!contains(github.event.pull_request.labels.*.name, 'release')" ]
  [ "$code_if" = "needs.plan.outputs.code == 'true'" ]
  [ "$docs_if" = "needs.plan.outputs.code == 'false'" ]
  [ "$docs_run" = 'make bats-unit-tests' ]
  [ "$controller_if" = "needs.plan.outputs.code == 'true'" ]
}

@test "the trigger map and the PR-template checklist list the same repositories" {
  [ -f "$TEMPLATE_FILE" ] || { echo "missing $TEMPLATE_FILE" >&2; exit 1; }

  # Checklist lines look like: - [ ] [cozystack/website](https://...) - follow-up:
  template_repos="$(grep -oE '^- \[ \] \[cozystack/[a-z0-9.-]+\]' "$TEMPLATE_FILE" \
    | grep -oE 'cozystack/[a-z0-9.-]+' \
    | sort -u)"

  # Map headings look like: ### cozystack/website
  map_repos="$(map_section \
    | grep -oE '^### cozystack/[a-z0-9.-]+' \
    | grep -oE 'cozystack/[a-z0-9.-]+' \
    | sort -u)"

  template_count="$(printf '%s\n' "$template_repos" | grep -c . || true)"
  [ "$template_count" -ge 1 ] || { echo "Found no cozystack/* checklist entries in $TEMPLATE_FILE - extraction is broken" >&2; exit 1; }

  if [ "$template_repos" != "$map_repos" ]; then
    echo "The PR-template checklist and the trigger map disagree on which repositories are downstream." >&2
    echo "In the checklist (.github/PULL_REQUEST_TEMPLATE.md):" >&2
    printf '%s\n' "$template_repos" | sed 's/^/  /' >&2
    echo "In the map (docs/agents/contributing.md, '### cozystack/<repo>' headings):" >&2
    printf '%s\n' "$map_repos" | sed 's/^/  /' >&2
    echo "Fix: add the repository to both, or remove it from both." >&2
    echo "A repository listed in only one of them gets ticked with no guidance, or documented with no box to tick." >&2
    exit 1
  fi

  echo "Checklist and trigger map agree on $template_count repositories"
}
