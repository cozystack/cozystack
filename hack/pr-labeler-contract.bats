#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Contract for the `backport` default in .github/workflows/pr-labeler.yaml.
#
# The labeler applies two classes of label and they are not the same kind of
# thing. `kind/*` and `area/*` are DERIVATIONS from the PR title: they are
# re-applied on every run of the workflow, which is correct, because a human
# removing `kind/bug` from a `fix(...)` PR is removing a fact. `backport` is a
# human DECISION with a default: the default is "yes" because forgetting the
# label costs a release-branch fix, and the escape hatch is that a maintainer
# removes it during review.
#
# Everything here exists to protect that escape hatch. The workflow runs on
# `opened`, `reopened` and `synchronize`, so re-applying `backport` the way the
# derivations are re-applied would bring it back with the next push — removal
# would be fiction, and worse than no default at all because it would look like
# it works. The label is therefore seeded only on `opened`, which fires exactly
# once per PR. That asymmetry is a feature, it reads like an inconsistency, and
# the obvious "cleanup" of it is a silent regression that no run reports: the
# labels still look right on the PR that gets one, and the PR whose label was
# removed simply gets backported anyway.
#
# WHY THIS EXECUTES THE LOGIC RATHER THAN GREPPING FOR IT. The decision lives in
# a `github-script` step, so a bats file can reach it in two ways. A shape pin
# (grep for the guard) is cheap and does catch the regression named above, since
# folding `backport` in with the derivations necessarily deletes the line. It
# also cannot tell a live guard from a dead one: reordered behind an `||`, or an
# exclusion entry present in the list and never read, both pass a grep. So the
# primary tests below EXTRACT the step's script verbatim out of the YAML and run
# it under node the way github-script does — an async function over
# (github, context, core) — with stubbed API and a synthetic event payload. No
# mirror of the logic is kept here, so there is nothing to drift.
#
# The synthetic payload is faithful in the fields the script reads. `action` at
# the top level of the payload, with the values `opened` / `reopened` /
# `synchronize`, was checked against GitHub's own recorded pull_request webhook
# deliveries (octokit/webhooks payload-examples), and against this repository's
# existing use of `github.event.action` on `pull_request_target` in
# backport.yaml and pull-requests.yaml.
#
# WHERE NODE IS MISSING. `make unit-tests` runs on a self-hosted ephemeral
# runner and node is not guaranteed on its PATH, so each behavioural test
# degrades to a soft skip, as hack/release-changelog-behaviour.bats and
# hack/release-freeze-contract.bats already do. A skip that leaves zero coverage
# is the failure mode of that pattern, so the structural pins at the bottom of
# this file deliberately duplicate the invariant in a form that needs no runtime
# and always runs: the guard is present and executable, it is an equality on
# `opened` rather than a negation that fails open when `types:` widens, `opened`
# is still a delivered type, the exclusion list holds what the contract names and
# is actually read, and the label exists in the repository.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
WF="$REPO_ROOT/.github/workflows/pr-labeler.yaml"
LABELS="$REPO_ROOT/.github/labels.yml"

# Strip JavaScript comments. Every constant and guard pinned below is discussed
# by name in the script's own comments, so filtering on `//` is what keeps a
# comment from satisfying a pin. POSIX grep only: the unit-test runner has no
# ripgrep. grep exits 1 when nothing is selected and 2 on a real error; this is
# never the last stage of a pipeline here, so what catches a real error is the
# emptied stream failing a presence pin, not the status.
script_lines() {
  local rc=0
  grep -v '^[[:space:]]*//' || rc=$?
  [ "$rc" -le 1 ]
}

have_node() {
  command -v node >/dev/null 2>&1
}

# The scopes excluded from the default, one per line, read out of the workflow
# rather than duplicated here — a list this file also carried would let the two
# drift apart and call that agreement.
excluded_scopes() {
  script_lines < "$WF" |
    awk '/const NO_BACKPORT_SCOPES = new Set\(\[/ { inside = 1; next } /\]\);/ { inside = 0 } inside' |
    tr -d ' ' | tr ',' '\n' | sed -n "s/^'\([^']*\)'.*/\1/p"
}

# Run the workflow's own script over a list of cases and print one verdict per
# line, `yes [labels]` or `no [labels]`, where the yes/no is whether `backport`
# was among the labels the run would have applied and [labels] is everything it
# would have applied, sorted.
#
# One case per argument, four `|`-separated fields: action, title, body (with
# `\n` for a line break), comma-separated labels the PR already carries. All
# cases go through ONE node invocation: driving them from a shell loop has
# produced an empty capture under this repo's cozytest runner (see
# hack/release-changelog-behaviour.bats), and a table computed in one process
# compares as a whole anyway.
#
# Every line of the heredoc is indented, including closing braces. cozytest.sh's
# translator rewrites any line that is exactly `}` into `return 0` followed by
# `}` — it is line-based over the whole file and does not know it is inside a
# heredoc, so a JS block closing at column 0 would have a statement injected into
# it.
decide() {
  local tmp
  tmp="$(mktemp -d)"
  cat > "$tmp/decide.js" <<'JS'
  const fs = require('fs');

  // Extract the `script: |` block verbatim and dedent it. This file has exactly
  // one such block; a second would need this to name its step.
  const lines = fs.readFileSync(process.argv[2], 'utf8').split('\n');
  const start = lines.findIndex((l) => /^\s*script:\s*\|\s*$/.test(l));
  if (start < 0) {
    console.error('no `script: |` block in ' + process.argv[2]);
    process.exit(1);
  }
  const indent = lines[start + 1].match(/^ */)[0].length;
  const src = [];
  for (let i = start + 1; i < lines.length; i++) {
    const l = lines[i];
    if (l.trim() === '') {
      src.push('');
      continue;
    }
    if (l.match(/^ */)[0].length < indent) {
      break;
    }
    src.push(l.slice(indent));
  }

  // github-script wraps the body in an async function taking exactly these
  // names, which is why top-level `await` and `return` work in the workflow.
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  const fn = new AsyncFunction('github', 'context', 'core', src.join('\n'));

  async function verdict(spec) {
    const f = spec.split('|');
    const applied = [];
    const github = {
      rest: { issues: { addLabels: async (a) => { applied.push(...a.labels); } } },
    };
    const context = {
      payload: {
        action: f[0],
        pull_request: {
          number: 1,
          title: f[1],
          body: (f[2] || '').replace(/\\n/g, '\n'),
          labels: (f[3] || '').split(',').filter(Boolean).map((n) => ({ name: n })),
        },
      },
      repo: { owner: 'cozystack', repo: 'cozystack' },
      eventName: 'pull_request_target',
    };
    const core = {
      info: () => {},
      warning: () => {},
      setFailed: (m) => { throw new Error('setFailed: ' + m); },
    };
    await fn(github, context, core);
    const flag = applied.includes('backport') ? 'yes' : 'no';
    return flag + ' [' + applied.slice().sort().join(',') + ']';
  }

  (async () => {
    const out = [];
    for (const spec of process.argv.slice(3)) {
      out.push(await verdict(spec));
    }
    fs.writeFileSync(1, out.join('\n') + '\n');
  })().catch((e) => {
    console.error(e);
    process.exit(1);
  });
JS
  node "$tmp/decide.js" "$WF" "$@"
  rm -rf "$tmp"
}

# Compare a verdict table against what the contract requires, printing both on a
# mismatch. `want` first so a failure reads as "expected ... got ...".
expect_table() { # <want> <got>
  [ "$1" = "$2" ] || {
    echo "verdict table mismatch." >&2
    echo "want:" >&2; printf '%s\n' "$1" | sed 's/^/  /' >&2
    echo "got:"  >&2; printf '%s\n' "$2" | sed 's/^/  /' >&2
    exit 1
  }
}

@test "the workflow under contract exists" {
  [ -f "$WF" ]
}

# ── the default ──────────────────────────────────────────────────────────────

@test "a fix PR gets the backport label when it is opened" {
  have_node || { echo "node unavailable; skipping"; return 0; }

  # The whole point of the change: nobody has to remember the label.
  want='yes [area/database,backport,kind/bug]'
  expect_table "$want" "$(decide 'opened|fix(postgres): stop the operator dropping the role||')"
}

@test "a removed backport label is never re-applied on a later push or reopen" {
  have_node || { echo "node unavailable; skipping"; return 0; }

  # THE load-bearing test. The first two cases are a maintainer's decision:
  # `backport` is absent from a `fix` PR that already carries its derivations,
  # which is exactly the state a removal leaves behind. Re-adding it there makes
  # removal impossible, so both must apply nothing at all.
  #
  # The last two are the same actions on a PR that never had the label — they
  # pin that the answer is "not on this action", not "not when some label
  # happens to be present", which is the reading a later edit could satisfy by
  # keying on the label set instead of the action.
  want='no []
no []
no [area/database,kind/bug]
no [area/database,kind/bug]'
  expect_table "$want" "$(decide \
    'synchronize|fix(postgres): stop the operator dropping the role||kind/bug,area/database' \
    'reopened|fix(postgres): stop the operator dropping the role||kind/bug,area/database' \
    'synchronize|fix(postgres): stop the operator dropping the role||' \
    'reopened|fix(postgres): stop the operator dropping the role||')"
}

@test "only a fix gets the default" {
  have_node || { echo "node unavailable; skipping"; return 0; }

  # Including `revert`, which is a mapped Conventional Commits type carrying no
  # kind/* of its own, and the bracket-form fallback, which infers no type at all
  # and so must not be read as a fix.
  want='no [area/database,kind/feature]
no [area/dependencies,kind/cleanup]
no [area/uncategorized]
no [area/database]'
  expect_table "$want" "$(decide \
    'opened|feat(postgres): add a knob||' \
    'opened|chore(deps): bump something||' \
    'opened|revert: undo something||' \
    'opened|[postgres] something||')"
}

# ── the three exclusions ─────────────────────────────────────────────────────

@test "a breaking fix never defaults into a patch release line" {
  have_node || { echo "node unavailable; skipping"; return 0; }

  # All three ways the labeler learns a change is breaking. A release line takes
  # patches; a breaking change is not one, whatever its Conventional Commits
  # type says.
  want='no [area/api,kind/breaking-change,kind/bug]
no [area/api,kind/breaking-change,kind/bug]
no [area/api,kind/breaking-change,kind/bug]'
  expect_table "$want" "$(decide \
    'opened|fix(api)!: require a dot-free prefix||' \
    'opened|fix(api): require a dot-free prefix|BREAKING CHANGE: no longer accepted|' \
    'opened|fix(api): require a dot-free prefix|BREAKING-CHANGE: no longer accepted|')"
}

@test "a backport PR is not itself labeled for backport" {
  have_node || { echo "node unavailable; skipping"; return 0; }

  # The backport bot titles its PR `[Backport release-X.Y] <original>`, so the
  # original's `fix` type survives into the derivation. Labeling it is what
  # caused recursive backports (docs/release.md); `area/release` is still due.
  want='no [area/database,area/release,kind/bug]'
  expect_table "$want" \
    "$(decide 'opened|[Backport release-1.6] fix(postgres): stop the operator dropping the role||')"
}

@test "every scope on the exclusion list suppresses the default" {
  # Structural half first, so this test is not vacuous where node is missing.
  scopes="$(excluded_scopes)"
  [ -n "$scopes" ]

  # The nine the contract names. A scope dropping off the list is a fix to
  # main-only code defaulting onto a release branch, which nothing reports.
  for s in ci e2e tests build deps agents release backport migrations; do
    printf '%s\n' "$scopes" | grep -qxF "$s"
  done

  # And the list has to be read. A set nothing consults satisfies every pin
  # above while excluding nothing.
  script_lines < "$WF" | grep -qF 'NO_BACKPORT_SCOPES.has(s)'

  have_node || { echo "node unavailable; structural half above still ran"; return 0; }

  # Drive the harness from the list in the file rather than from a copy, so a
  # scope added there is exercised without editing this test. The leading
  # non-excluded case is the non-vacuity check: if the harness said "no" to
  # everything the loop below would pass while proving nothing.
  #
  # `_` rather than a space in every title here: the list is assembled into one
  # string and expanded unquoted, so a space would split one case into two.
  cases='opened|fix(postgres):_x||'
  for s in $scopes; do
    cases="$cases opened|fix($s):_x||"
  done
  # shellcheck disable=SC2086
  got="$(decide $cases)"

  first="$(printf '%s\n' "$got" | sed -n '1p')"
  [ "$first" = 'yes [area/database,backport,kind/bug]' ]

  rest="$(printf '%s\n' "$got" | sed -n '2,$p')"
  [ "$(printf '%s\n' "$rest" | grep -c .)" -eq "$(printf '%s\n' "$scopes" | grep -c .)" ]

  # Capture grep's status rather than writing `! grep`, which cannot fail a test
  # in either runner reliably (see hack/issue-triage-contract.bats).
  rc=0
  printf '%s\n' "$rest" | grep -q '^yes' || rc=$?
  [ "$rc" -ne 0 ]
}

@test "one excluded scope in a composite title suppresses the default" {
  have_node || { echo "node unavailable; skipping"; return 0; }

  # ANY excluded part suppresses, in either order, rather than all of them
  # having to be excluded: `fix(platform,ci)` is the shape that drags main-only
  # content into a release branch. The third case pins that the suppression is
  # the excluded scope's doing and not composite scopes in general.
  want='no [area/ci,area/platform,kind/bug]
no [area/ci,area/platform,kind/bug]
yes [area/monitoring,area/platform,backport,kind/bug]'
  expect_table "$want" "$(decide \
    'opened|fix(platform,ci): two things||' \
    'opened|fix(ci,platform): two things||' \
    'opened|fix(platform,monitoring): two things||')"
}

# ── nothing that was already there moved ─────────────────────────────────────

@test "kind/* and area/* still apply on every action" {
  have_node || { echo "node unavailable; skipping"; return 0; }

  # The derivations are re-applied on every run and must stay that way: gating
  # the whole labeler on `opened` would be the lazy way to satisfy every test
  # above and would stop a retitled PR from ever being relabeled.
  want='no [area/database,kind/feature]
no [area/release,kind/documentation]
no [area/api,kind/breaking-change,kind/bug]
no [area/uncategorized]'
  expect_table "$want" "$(decide \
    'synchronize|feat(postgres): add a knob||' \
    'reopened|docs(release): reword||' \
    'synchronize|fix(api)!: require a dot-free prefix||' \
    'opened|not a conventional title at all||')"
}

# ── pins that need no runtime ────────────────────────────────────────────────

@test "the default is gated on the opened action" {
  # Equality on `opened`, in an executable line rather than a comment. This is
  # the one line whose deletion turns removal of the label into fiction.
  script_lines < "$WF" | grep -qF "context.payload.action === 'opened'"

  # And NOT a negation. `!== 'synchronize'` reads as equivalent only while
  # `types:` holds exactly the three it holds today: it fails open, handing the
  # default to every type added later — `edited` and `ready_for_review` both fire
  # repeatedly on one PR, so either would resurrect a removed label.
  rc=0
  script_lines < "$WF" | grep -qF 'payload.action !==' || rc=$?
  [ "$rc" -ne 0 ]
}

@test "opened is still a delivered event type" {
  # The default is seeded on exactly one delivery, so dropping `opened` from
  # `types:` silently retires the feature while leaving every line of it in
  # place. Comments are stripped: the note above `types:` names the actions.
  grep -v '^[[:space:]]*#' < "$WF" | grep -qE '^[[:space:]]*types:.*[[:space:]]opened[,]?' ||
    grep -v '^[[:space:]]*#' < "$WF" | grep -qE '^[[:space:]]*types:.*\[opened[,]?'
}

@test "the other two exclusions are executable, not commentary" {
  # Both reuse state the script already computed — the `[Backport ...]` detector
  # from step 1 and the `!` / footer parse — so what is pinned is that the
  # condition still consults them.
  script_lines < "$WF" | grep -qF '!backportMatch'
  script_lines < "$WF" | grep -qF '!breaking'
}

@test "the backport label is declared in the repository" {
  [ -f "$LABELS" ]

  # addLabels creates a label that does not exist, with a default colour and no
  # description, so a typo here is invisible until someone notices two
  # almost-identically named labels. Whole-line match, because a substring match
  # accepts a truncation and a truncation is what a typo looks like:
  # `name: backport` occurs inside `- name: backport-previous`, which is a
  # different label with a different target and is deliberately left manual.
  # `-e`, because the pattern opens with the list dash.
  grep -v '^[[:space:]]*#' < "$LABELS" | sed 's/[[:space:]]*#.*$//; s/[[:space:]]*$//' |
    grep -qxF -e '- name: backport'

  # The label the script adds, read from the script, so a rename there has to be
  # matched in labels.yml rather than silently creating a second label.
  script_lines < "$WF" | grep -qF "toAdd.add('backport')"
}
