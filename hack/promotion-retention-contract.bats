#!/usr/bin/env bats
# Structural contract for abandoned stable-candidate artifact retention.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
RETENTION="$REPO_ROOT/.github/workflows/retention.yaml"

code_lines() {
  rc=0
  grep -v '^[[:space:]]*#' || rc=$?
  [ "$rc" -le 1 ]
}

step_block() {
  awk -v want="      - name: $1" '
    $0 == want { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside' "$2"
}

@test "retention deletes only old packages versions carrying temporary tags alone" {
  block="$(step_block 'Prune' "$RETENTION")"
  [ -n "$block" ]

  printf '%s\n' "$block" | code_lines | grep -qF "pkg='cozystack/cozystack-packages'"
  printf '%s\n' "$block" | code_lines | grep -qF 'select((.metadata.container.tags | length) > 0)'
  printf '%s\n' "$block" | code_lines | grep -qF 'select(.metadata.container.tags | all(test("^promotion-v'
  printf '%s\n' "$block" | code_lines | grep -qF -- '-run-[0-9]+-[0-9]+$'
  printf '%s\n' "$block" | code_lines | grep -qF 'select(.updated_at < $cutoff)'
  printf '%s\n' "$block" | code_lines | grep -qF 'index($digest)) == null'
}

@test "retention protects candidates pinned by open bot release PRs" {
  block="$(step_block 'Prune' "$RETENTION")"
  [ -n "$block" ]

  printf '%s\n' "$block" | code_lines | grep -qF 'pulls?state=open&per_page=100'
  printf '%s\n' "$block" | code_lines | grep -qF '.user.login == "cozystack-ci[bot]"'
  printf '%s\n' "$block" | code_lines | grep -qF '.head.repo.full_name == $repo'
  printf '%s\n' "$block" | code_lines | grep -qF 'any(.labels[]?; .name == "release")'
  printf '%s\n' "$block" | code_lines | grep -qF '/^[\047"]|[\047"][[:space:]]*$/'
  printf '%s\n' "$block" | code_lines | grep -qF "grep -Eq '^digest=sha256:[0-9a-f]{64}$'"
  printf '%s\n' "$block" | code_lines | grep -qF 'refusing retention'
}

@test "retention also protects candidates pinned by a base-branch tip" {
  # The open PR is not the whole danger window. A candidate stops being
  # eligible only once finalize's retag puts a stable tag on its manifest, and
  # finalize runs after the merge that closes the PR — so between those two
  # points the branch tip is the only thing still naming the digest.
  block="$(step_block 'Prune' "$RETENTION")"
  [ -n "$block" ]

  printf '%s\n' "$block" | code_lines | grep -qF 'branches?per_page=100'
  printf '%s\n' "$block" | code_lines \
    | grep -qF 'select(. == "main" or test("^release-[0-9]+\\.[0-9]+$"))'
  printf '%s\n' "$block" | code_lines | grep -qF "grep -oE 'digest=sha256:[0-9a-f]{64}'"
}

@test "an unresolved pin stops the promotion sweep and nothing else" {
  # Fail-closed for deletion is right; taking the unrelated nightly sweep down
  # with it is not. A WIP commit adding a trailing comment to a pin on a
  # release branch would otherwise silently stop GHCR nightly pruning.
  block="$(step_block 'Prune' "$RETENTION")"
  [ -n "$block" ]

  printf '%s\n' "$block" | code_lines | grep -qF 'promotion_retention_blocked=1'
  printf '%s\n' "$block" | code_lines | grep -qF 'skipping the promotion sweep'

  # The guard has to sit between the nightly sweep and the promotion sweep:
  # ahead of the nightly one it is the coupling it exists to remove, behind the
  # promotion one it does not guard it.
  nightly="$(printf '%s\n' "$block" | code_lines \
    | grep -nF 'nightlies, keep ${KEEP}, drop' | awk -F: 'NR == 1 { print $1 }')"
  guard="$(printf '%s\n' "$block" | code_lines \
    | grep -nF 'skipping the promotion sweep' | awk -F: 'NR == 1 { print $1 }')"
  sweep="$(printf '%s\n' "$block" | code_lines \
    | grep -nF "pkg='cozystack/cozystack-packages'" | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$nightly" ] && [ -n "$guard" ] && [ -n "$sweep" ]
  [ "$nightly" -lt "$guard" ]
  [ "$guard" -lt "$sweep" ]
}

@test "a listing failure defers to that guard instead of aborting the job" {
  # The deferred guard above is only worth having if nothing between it and the
  # protection sources can exit first. Both sources open with a `gh api
  # --paginate` listing that sits ABOVE the nightly sweep, and the step runs
  # under `set -euo pipefail`: unhandled, a transient /pulls or /branches blip
  # exits the step and takes unrelated GHCR nightly pruning down with it.
  block="$(step_block 'Prune' "$RETENTION")"
  [ -n "$block" ]

  printf '%s\n' "$block" | code_lines | grep -qF 'set -euo pipefail'

  # Each listing must carry its own handler, which has to do both things: raise
  # the blocked flag, so deletion stays fail-closed, and empty the list, because
  # a half-paginated list left in place is a smaller protected set and a smaller
  # protected set is the one shape that can authorise a delete.
  #
  # Asserted within a short window after the listing rather than anywhere in the
  # step, so a handler belonging to a different failure path cannot satisfy it.
  # The window is why an edit that grows one of these jq filters has to move its
  # handler along with it.
  for pair in 'pulls?state=open&per_page=100|release_pr_shas' \
              'branches?per_page=100|base_branches'; do
    endpoint="${pair%%|*}"
    var="${pair#*|}"
    listing="$(printf '%s\n' "$block" | code_lines \
      | grep -nF "$endpoint" | awk -F: 'NR == 1 { print $1 }')"
    [ -n "$listing" ]
    window="$(printf '%s\n' "$block" | code_lines \
      | awk -v start="$listing" 'NR >= start && NR <= start + 11')"
    printf '%s\n' "$window" | grep -qF 'promotion_retention_blocked=1'
    printf '%s\n' "$window" | grep -qF "${var}=\"\""
  done
}

@test "unresolved package names or unreadable versions cannot exit green" {
  # The nightly sweep's package list used to come from an org listing whose
  # failure arrived as an empty list, and the empty state exited 0 above the
  # deferred guard, so a broken run could end green having swept nothing. The
  # names now come from the published tree, and the same hazard has two shapes:
  # an empty list that exits 0, and a versions read that `set -e` turns into an
  # abort of the whole loop. Positions are read inside short windows so an
  # exit or a flag belonging to another path cannot satisfy them.
  block="$(step_block 'Prune' "$RETENTION")"
  [ -n "$block" ]
  code="$(printf '%s\n' "$block" | code_lines)"

  # An empty list flags the run instead of exiting: the promotion sweep below
  # needs no name from that list, and its safety rests on the protected set
  # alone, so a broken name source must not stop it.
  resolve="$(printf '%s\n' "$code" | grep -nF 'retention-packages.sh' | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$resolve" ]
  branch="$(printf '%s\n' "$code" | awk -v start="$resolve" 'NR >= start && NR <= start + 8')"
  printf '%s\n' "$branch" | grep -qF 'pkgs=""'
  printf '%s\n' "$branch" | grep -qF '[ -z "$pkgs" ]'
  printf '%s\n' "$branch" | grep -qF 'sweep_failed=1'
  if printf '%s\n' "$branch" | grep -qE '(^|[^_])exit '; then
    echo "FAIL: an empty package list exits before the promotion sweep"
    false
  fi

  read_at="$(printf '%s\n' "$code" | grep -nF '/versions" 2>' | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$read_at" ]
  [ "$read_at" -gt "$resolve" ]
  handler="$(printf '%s\n' "$code" | awk -v start="$read_at" 'NR >= start && NR <= start + 8')"
  printf '%s\n' "$handler" | grep -qF 'sweep_failed=1'
  printf '%s\n' "$handler" | grep -qF 'continue'

  # The flag is honoured after the promotion sweep, the last work the run does.
  promotion_at="$(printf '%s\n' "$code" | grep -nF 'promotion_ids=' | awk -F: 'NR == 1 { print $1 }')"
  check_at="$(printf '%s\n' "$code" | grep -nF '"$sweep_failed" -eq 1' | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$promotion_at" ] && [ -n "$check_at" ]
  [ "$promotion_at" -lt "$check_at" ]
  # The exit sits inside the flagged branch and nothing follows "Done.": an
  # exit that fires unconditionally would turn every run red.
  verdict="$(printf '%s\n' "$code" | awk -v s="$check_at" 'NR > s && NR <= s + 4 { sub(/^[[:space:]]+/, ""); print }')"
  [ "$(printf '%s\n' "$verdict" | sed -n 1p | grep -cF 'echo "::error::')" -eq 1 ]
  [ "$(printf '%s\n' "$verdict" | sed -n 2p)" = 'exit 1' ]
  [ "$(printf '%s\n' "$verdict" | sed -n 3p)" = 'fi' ]
  [ "$(printf '%s\n' "$verdict" | sed -n 4p)" = 'echo "Done."' ]
  [ -z "$(printf '%s\n' "$code" | awk -v s="$check_at" 'NR > s + 4' | grep -v '^[[:space:]]*$')" ]
}

@test "an unreadable candidate listing fails the run without deleting anything" {
  # Unhandled, `set -e` aborted the step on a bare gh error. Handled, the read
  # must also leave no ids behind, because a partial page set is a list the
  # protection filter never saw in full.
  block="$(step_block 'Prune' "$RETENTION")"
  [ -n "$block" ]
  code="$(printf '%s\n' "$block" | code_lines)"

  read_at="$(printf '%s\n' "$code" | grep -nF 'promotion_ids="$(gh api' | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$read_at" ]
  handler="$(printf '%s\n' "$code" | awk -v start="$read_at" 'NR >= start && NR <= start + 14')"
  printf '%s\n' "$handler" | grep -qF '::error::'
  printf '%s\n' "$handler" | grep -qF 'sweep_failed=1'
  printf '%s\n' "$handler" | grep -qF "promotion_ids='[]'"
}

@test "a refused delete flags the run instead of aborting the sweep" {
  # Unflagged, a failed xargs would abort the step under `set -e`: still red,
  # but the remaining packages and the promotion sweep would never run.
  block="$(step_block 'Prune' "$RETENTION")"
  [ -n "$block" ]
  calls="$(printf '%s\n' "$block" | code_lines | grep -F 'retention-delete.sh' || true)"
  [ "$(printf '%s\n' "$calls" | grep -c .)" -ge 2 ]
  if printf '%s\n' "$calls" | grep -vqE '\|\| sweep_failed=1$'; then
    echo "FAIL: a delete call does not flag the run: $calls"
    false
  fi
}
