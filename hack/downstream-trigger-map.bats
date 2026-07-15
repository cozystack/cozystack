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
# exists; the workflow carve-out that keeps this suite running on a docs-only PR
# to the map's own location; and the map and the PR-template checklist to the same
# repository list, so a repository cannot be listed in one and forgotten in the
# other.
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
# A docs-only PR normally skips the unit tests, which would have exempted the map
# from its own guard; the plan step in .github/workflows/pull-requests.yaml carves
# out docs/agents/contributing.md so that editing the map alone still runs these.

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

@test "the workflow opts the trigger map back into the unit tests" {
  WORKFLOW="$REPO_ROOT/.github/workflows/pull-requests.yaml"
  [ -f "$WORKFLOW" ] || { echo "missing $WORKFLOW" >&2; exit 1; }

  # Every check below reads the workflow with comments stripped. A commented-out
  # line still contains its own text, so grepping the raw file would accept a
  # carve-out that had been disabled and left behind as a TODO — the most likely
  # way this actually rots.
  code_only="$(sed 's/#.*//' "$WORKFLOW")"

  # The block of the job that runs the unit tests, from its header to the next
  # job's. The condition has to sit on THAT job: pinning it file-wide would accept
  # the exact expression pasted into some other job while the unit-test job quietly
  # loses it.
  unit_job="$(printf '%s\n' "$code_only" | awk '
    /^  [a-zA-Z0-9_-]+:[[:space:]]*$/ { inside = ($0 == "  checks:") }
    inside')"
  printf '%s\n' "$unit_job" | grep -q 'make unit-tests' || {
    echo "The 'checks' job in .github/workflows/pull-requests.yaml no longer runs 'make unit-tests'." >&2
    echo "This suite pins the trigger-map carve-out to that job. If the unit tests moved, point" >&2
    echo "the checks below at their new job." >&2
    exit 1
  }

  # Three links carry the carve-out: the plan step names the map, the plan job
  # exports the flag, and the unit-test job gates on it. Cutting any one leaves the
  # other two looking perfectly wired, and none of them fails loudly — an unset or
  # unexported output dereferences to an empty string, so the condition is merely
  # false and the suite is skipped in silence, on exactly the PRs it guards, with
  # every test in this file still green. Pin all three.
  rel="${MAP_FILE#"$REPO_ROOT"/}"
  printf '%s\n' "$code_only" | grep -qF "'$rel'" || {
    echo "The plan step in .github/workflows/pull-requests.yaml does not name '$rel'." >&2
    echo "Without it, a PR that only edits the trigger map is treated as docs-only, the unit" >&2
    echo "tests are skipped, and this file never runs — exactly when it is needed most." >&2
    echo "Fix: point the trigger_map detection at the map's new path." >&2
    exit 1
  }

  printf '%s\n' "$code_only" | grep -qF 'trigger_map: ${{ steps.p.outputs.trigger_map }}' || {
    echo "The plan job in .github/workflows/pull-requests.yaml does not export trigger_map." >&2
    echo "The step still computes it and the unit-test job still reads it, so this looks wired" >&2
    echo "up, but an unexported output dereferences to an empty string: the job is skipped in" >&2
    echo "silence on exactly the PRs this suite guards." >&2
    echo "Fix: restore 'trigger_map: \${{ steps.p.outputs.trigger_map }}' to the plan job's outputs." >&2
    exit 1
  }

  condition="(needs.plan.outputs.code == 'true' || needs.plan.outputs.trigger_map == 'true')"
  printf '%s\n' "$unit_job" | grep -qF "$condition" || {
    echo "The job that runs 'make unit-tests' is not gated on:" >&2
    echo "  $condition" >&2
    echo "Mentioning trigger_map in another job, or in a comment, does not count. Without that" >&2
    echo "exact condition on that job, a PR which only edits the trigger map stays docs-only," >&2
    echo "skips the unit tests, and never runs this file — while every test here still passes." >&2
    exit 1
  }

  echo "The workflow names '$rel', exports the flag, and gates the unit tests on it"
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
