#!/usr/bin/env bats
# Contract for the PR title labeler in .github/workflows/pr-labeler.yaml.
#
# Every failure this guards is silent: the job either dies on a title it cannot
# handle, applies a label that does not exist, or lands a PR in the wrong area
# with no warning, and nobody reads the log of a labeler that did not label.
#
# The behavioural tests run the workflow's own github-script block under node,
# extracted from the file rather than mirrored, with `context`, `core` and
# `github` stubbed, so an edit to the script is what gets tested. node is
# required, not optional: a suite that skips when the runtime is missing goes
# green having run nothing.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
WF="$REPO_ROOT/.github/workflows/pr-labeler.yaml"
LABELS="$REPO_ROOT/.github/labels.yml"

need_node() {
  command -v node >/dev/null 2>&1 && return 0
  echo "node is required to run the pr-labeler script" >&2
  return 1
}

# The `script: |` block, de-indented to plain JavaScript.
extract_script() {
  awk 'f { print substr($0, 13) } /^ +script: \|$/ { f = 1 }' "$WF" > "$1"
  [ -s "$1" ]
}

# Runs the extracted script once per title read from stdin, all in one node
# process, and prints one line per title: the title, a tab, then the applied
# labels sorted and comma-joined, or THROW: and the error message.
#
# JavaScript closing braces below are indented on purpose: hack/cozytest.sh
# rewrites any line that is exactly `}` into a shell `return 0`.
run_labeler() {
  tmp="$(mktemp -d)"
  extract_script "$tmp/script.js"
  cat > "$tmp/run.js" <<'JS'
const fs = require('fs');
const src = fs.readFileSync(process.argv[2], 'utf8');
const AsyncFunction = (async () => {}).constructor;
const fn = new AsyncFunction('context', 'core', 'github', src);
(async () => {
  const out = [];
  const titles = fs.readFileSync(0, 'utf8').split('\n').filter(Boolean);
  for (const title of titles) {
    let applied = [];
    const context = {
      repo: { owner: 'o', repo: 'r' },
      payload: { pull_request: { number: 1, title, body: '', labels: [] } },
    };
    const core = { warning() {}, info() {} };
    const github = { rest: { issues: { addLabels: async (a) => { applied = a.labels; } } } };
    try {
      await fn(context, core, github);
      out.push(title + '\t' + [...applied].sort().join(','));
    } catch (e) {
      out.push(title + '\tTHROW: ' + e.message);
    }
  }
  fs.writeFileSync(1, out.join('\n') + '\n');
})();
JS
  node "$tmp/run.js" "$tmp/script.js"
  rm -rf "$tmp"
}

# Asserts the labeler's output for each `title<TAB>labels` line on stdin.
expect_labels() {
  want="$(cat)"
  got="$(printf '%s\n' "$want" | cut -f1 | run_labeler)"
  [ "$got" = "$want" ] || {
    echo "labeler output mismatch." >&2
    echo "want:" >&2; printf '%s\n' "$want" | sed 's/^/  /' >&2
    echo "got:" >&2; printf '%s\n' "$got" | sed 's/^/  /' >&2
    return 1
  }
}

# The scopeToArea literal as JSON pairs, plus a count of the key lines in the
# source, quoted either way or bare. A duplicate key collapses when the literal
# is evaluated, so the two disagree when a key is written twice, as long as each
# key sits on its own line.
scope_map() { # json|keylines
  tmp="$(mktemp -d)"
  extract_script "$tmp/script.js"
  cat > "$tmp/map.js" <<'JS'
const fs = require('fs');
const src = fs.readFileSync(process.argv[2], 'utf8');
const m = src.match(/const scopeToArea = (\{[\s\S]*?\n\};)/);
if (!m) { throw new Error('scopeToArea literal not found'); }
const lines = m[1].split('\n').filter((l) => /^\s*(['"]?)[^'"\s:]+\1\s*:/.test(l));
const map = new Function('return ' + m[1].replace(/;$/, ''))();
if (process.argv[3] === 'keylines') {
  fs.writeFileSync(1, lines.length + ' ' + Object.keys(map).length + '\n');
} else {
  fs.writeFileSync(1, Object.entries(map).map(([k, v]) => k + '\t' + v).join('\n') + '\n');
  }
JS
  node "$tmp/map.js" "$tmp/script.js" "$1"
  rm -rf "$tmp"
}

area_labels() {
  sed -n 's/^- name: \(area\/[^[:space:]]*\)[[:space:]]*$/\1/p' "$LABELS"
}

@test "the workflow under contract exists" {
  [ -f "$WF" ]
  [ -f "$LABELS" ]
}

# A key inherited from Object.prototype resolves to a function, which is truthy,
# lands in the label set, and then kills the job on `l.startsWith`.
@test "a scope or type named after an Object.prototype member does not kill the job" {
  need_node
  expect_labels <<'EOF'
fix(constructor): x	area/uncategorized,kind/bug
fix(toString): x	area/uncategorized,kind/bug
fix(hasOwnProperty): x	area/uncategorized,kind/bug
fix(__proto__): x	area/uncategorized,kind/bug
[constructor] x	area/uncategorized
constructor: x	area/uncategorized
EOF
}

# One stripped prefix left the second one to be parsed as a bracket scope, which
# dropped the real scope and the kind, and the area/release from the first strip
# suppressed the uncategorized signal.
@test "every leading backport prefix is stripped before the title is parsed" {
  need_node
  expect_labels <<'EOF'
[Backport release-1.3] fix(linstor): x	area/release,area/storage,kind/bug
[Backport release-1.2] [Backport release-1.3] fix(linstor): x	area/release,area/storage,kind/bug
[Backport release-1.2] [Backport release-1.3] [Backport release-1.4] feat(linstor): x	area/release,area/storage,kind/feature
EOF
}

@test "an unmapped scope still falls back to area/uncategorized" {
  need_node
  expect_labels <<'EOF'
fix(no-such-scope): x	area/uncategorized,kind/bug
fix: x	area/uncategorized,kind/bug
just a sentence	area/uncategorized
EOF
}

@test "recurring scopes resolve to their area" {
  need_node
  expect_labels <<'EOF'
fix(etcd-operator): x	area/database,kind/bug
fix(opensearch): x	area/database,kind/bug
fix(multus): x	area/networking,kind/bug
fix(kubeovn): x	area/networking,kind/bug
fix(kubevirt-operator): x	area/virtualization,kind/bug
fix(keycloak-configure): x	area/keycloak,kind/bug
docs(changelog): x	area/release,kind/documentation
fix(lineage): x	area/platform,kind/bug
fix(piraeus-operator): x	area/storage,kind/bug
fix(monitoring-agents): x	area/monitoring,kind/bug
ci(workflows): x	area/ci
fix(hack): x	area/dx,kind/bug
EOF
}

# Contributors reach for the subsystem name as often as for a package name, so
# every area is reachable by its own name.
@test "every area label is accepted as a scope that maps to itself" {
  need_node
  map="$(scope_map json)"
  missing=""
  for a in $(area_labels); do
    [ "$a" = "area/uncategorized" ] && continue
    printf '%s\n' "$map" | grep -qxF "${a#area/}	$a" || missing="$missing ${a#area/}"
  done
  [ -z "$missing" ] || { echo "scopes missing for their own area:$missing" >&2; return 1; }
}

# A target missing from labels.yml is a label the sync workflow never creates,
# and nothing in a run of the labeler reports it.
@test "every scopeToArea target is an area label defined in labels.yml" {
  need_node
  defined="$(area_labels)"
  [ -n "$defined" ]
  bad="$(scope_map json | cut -f2 | sort -u | grep -vxF "$defined" || true)"
  [ -z "$bad" ] || { echo "targets not in labels.yml: $bad" >&2; return 1; }
}

# A repeated key silently overrides the earlier value and reads as an ordinary
# added line in review.
@test "no scope key appears twice in scopeToArea" {
  need_node
  read -r lines keys <<EOF
$(scope_map keylines)
EOF
  [ "$lines" -gt 0 ]
  [ "$lines" = "$keys" ] || { echo "$lines key lines but $keys distinct keys" >&2; return 1; }
}
