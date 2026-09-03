#!/bin/sh
# Read a list of changed files (one per line) and emit space-separated suite
# names whose Chainsaw suites under hack/e2e-chainsaw/ should run.
#
# Usage: hack/select-e2e.sh <changed-files> [<sources-dir>]
# Defaults: sources-dir = packages/core/platform/sources
#
# Output:
#   - empty         no E2E impact, decided by one of the rules that select
#                   nothing: step 1 for docs/, dashboards/ and *.md, step 2 for
#                   a *.disabled Chainsaw suite and for an
#                   examples/backups/<app>/ with no suite, and
#                   inert_config_pattern for repo meta and agent config
#   - <suite names> selected per the PackageSource dependency graph
#   - full list     any path that affects all tests, OR an unrecognised
#                   packages/* path, OR a changed package that reaches no
#                   runnable suite through the graph, OR a path matching NEITHER
#                   the full-suite nor the inert list, OR a yq that failed to
#                   build the dependency graph (conservative fallbacks)
#   - nothing, and  the suite list itself is unavailable: find failed, or
#     a non-zero    hack/e2e-chainsaw holds no chainsaw-test.yaml. Unlike the
#     exit          yq case there is no fallback left to take — an empty list
#                   silently corrupts both the escalations, which would print
#                   it, and the backups rule, which membership-tests against
#                   it — so the script refuses to answer at all, and since both
#                   lanes run it under `bash -e` the step fails
#
# Every changed path must land in exactly one of the three selection classes by
# an explicit rule; the fourth outcome is not a classification but a refusal to
# produce one. An unclassified path escalates to the full suite rather than
# selecting nothing: both e2e lanes read an empty selection as "skip Chainsaw"
# and then post the required "E2E Tests" status green, so a silent default is a
# green gate with no suite run (#3392). When that escalation fires for a path
# that genuinely cannot affect e2e, add it to inert_config_pattern — do not
# widen the fall-through.
#
# "Empty" is not a synonym for inert_config_pattern. Several rules select
# nothing: step 1 for docs/, dashboards/ and *.md, step 2 for a *.disabled
# Chainsaw suite and for an examples/backups/<app>/ whose app has no suite, and
# the pattern itself. The property the lanes depend on is weaker than "matched
# the inert list" and it is the one to preserve: an empty selection is a
# decision some rule reached, never a path nothing looked at.
#
# Every branch that escalates names its cause on stderr, and that is a contract
# rather than a courtesy: the answer "run everything" is the same 21 suite names
# whichever rule produced it, so without a reason line the only way to learn why
# a pull request ran the whole suite is to re-derive the selection by hand. Four
# of them were silent before this rule, full_suite_pattern — the commonest cause
# by a wide margin — among them, which made the usual answer the one the log
# never had. A test asserts each line, because a reason that regresses to silence
# changes no selection and so is invisible to every other test here. Deliberately
# stated without a count: the number of escalating branches moves whenever a rule
# is added, and a comment carrying a stale one reads as authoritative.
#
# The lines go to stderr, never stdout. stdout is the suite list and both lanes
# parse it, so a reason line there would be read as a suite name.
#
# One caveat worth knowing before trusting that: inert_config_pattern makes
# .github/ inert as a whole directory, and the e2e workflows are exempted by
# NAME in full_suite_pattern, which is checked first. So a workflow added under
# .github/workflows/ that runs the suite is silently inert until someone adds it
# to that list — the fall-through never sees it. The enumeration is the guard,
# which is why it carries the `rg -l test-chainsaw` reminder; treat that comment
# as load-bearing rather than decorative.
set -eu

CHANGED="${1:?missing changed-files arg}"
SOURCES_DIR="${2:-packages/core/platform/sources}"

# Anything matching this pattern triggers the full Chainsaw suite. Per-suite
# edits (hack/e2e-chainsaw/<name>/...) are matched BEFORE this so editing one
# suite doesn't escalate to the full suite; the shared _lib helpers and the
# .chainsaw.yaml config affect every suite and DO escalate (handled inline).
#
# Three groups, all "cannot be scoped to one app":
#   - shipped code and its build inputs: packages/library, packages/core, the Go
#     trees (api, cmd, internal, pkg), the codegen under tools/, go.mod/go.sum,
#     the root Makefile, hack/*.mk (the tag/push/output flags of every image),
#     hack/buildkitd.toml (the builder every image is built with), and hack/lib/
#     (sourced by the hack/*.sh scripts that already escalate);
#   - the e2e harness itself: hack/*.sh, hack/e2e-*.bats and hack/e2e-*.yaml.
#     The bats half is prefix-matched rather than taking every hack/*.bats,
#     because the root Makefile splits those two sets by exactly that prefix —
#     `BATS_UNIT_FILES := $(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats))`
#     — so the 60 files it keeps are the unit lane and the e2e sandbox runs none
#     of them. The three it filters out are the ones packages/core/testing's
#     recipes execute inside the sandbox, and they stay here. Narrowing this
#     cannot leave a bats-only pull request untested: `make unit-tests` is gated
#     on the `code` output, which pull-requests.yaml computes as "any path
#     outside docs/" and never from this script, so those 60 files run on their
#     own lane whatever the selection here is;
#   - the workflows that RUN the suite — enumerated rather than matched by
#     prefix, so an unrelated workflow does not burn a full run. Keep this list
#     in step with `rg -l test-chainsaw .github/workflows/`.
#
#     What that costs, stated rather than left to be rediscovered: only
#     pull-requests.yaml is executed from the PR's own head. e2e-fork runs on
#     workflow_run, which always takes the default-branch copy; e2e-tag runs on
#     workflow_call from a tag push; nightly runs on a schedule. So for those
#     three the suite cannot exercise the edit under review, and escalating buys
#     generic regression coverage rather than coverage of the change. Measured
#     over the last six months of main: 26 commits touch one of the three with
#     no other escalating path, and each now pays a full Chainsaw run it would
#     previously have skipped. They stay on the list so the rule remains one
#     idea ("workflows that run the suite") rather than two, and because the
#     alternative leaves them inert, which reads as an oversight rather than a
#     decision. Reopen the trade if the full suite's flake rate makes the
#     generic coverage cost more than it returns.
full_suite_pattern='^(packages/library/|packages/core/|api/|cmd/|internal/|pkg/|tools/|hack/lib/|hack/[^/]+\.sh$|hack/e2e-[^/]+\.bats$|hack/[^/]+\.mk$|hack/buildkitd\.toml$|hack/e2e-[^/]+\.ya?ml$|go\.(mod|sum)$|Makefile$|\.github/workflows/(pull-requests|e2e-fork|e2e-tag|nightly)\.yaml$)'

# Paths with no bearing on what e2e exercises. Checked AFTER full_suite_pattern,
# so a specific escalation wins over a broad directory here (.github/ is inert,
# .github/workflows/pull-requests.yaml is not). This list is the whole reason
# the fall-through at the bottom of the loop can escalate safely: the cost of
# forgetting an inert path is a wasted full run, the cost of forgetting a live
# one used to be a green gate with nothing tested.
#   - examples/       demo manifests; examples/backups/<app>/ is handled above
#                     as a real test harness before this check is reached
#   - .github/        templates, CODEOWNERS, labels, renovate, linter config —
#                     minus the e2e workflows escalated above
#   - .claude/ .gemini/  agent config, never shipped
#   - img/            README assets
#   - hack/testdata/  fixtures for the bats unit tests, not for e2e
#   - hack/*.bats     the unit lane, minus the hack/e2e-*.bats escalated above.
#                     The root Makefile draws the line at that prefix
#                     (BATS_UNIT_FILES filters hack/e2e-%.bats out of
#                     hack/*.bats), so these 60 files are never executed inside
#                     the e2e sandbox and no Chainsaw suite can regress from one.
#                     They are not untested by being inert here: `make
#                     unit-tests` runs them, gated on pull-requests.yaml's `code`
#                     output, which is "any path outside docs/" and is computed
#                     in the workflow rather than from this script
#   - packages/tests/ helm-unittest fixture charts. cozy-lib-tests exercises
#                     library/cozy-lib from the outside; changing a test OF
#                     cozy-lib does not change cozy-lib, no PackageSource lists
#                     these paths as a component, and nothing installs them. A
#                     change to the library itself still escalates through
#                     packages/library/ in full_suite_pattern
#   - boilerplate.go.txt / dcgm-default-counters.csv  codegen header; a CSV read
#                     only by check-gpu-recording-rules.bats
#   - the three .gitattributes  ENUMERATED, not matched by name. Every
#                     .gitattributes in this tree holds nothing but
#                     `linguist-generated` markers — GitHub's language-stats and
#                     diff-collapsing hint, which reaches no build, chart or test
#                     — and these are the three whose classification the entry
#                     changes; the two under api/ and internal/ are escalated by
#                     full_suite_pattern first, so an entry for them would be
#                     dead. The justification is about what these files CONTAIN,
#                     and the file name does not carry it: .gitattributes can also
#                     set `filter`, `eol`, `working-tree-encoding` and
#                     `export-subst`, every one of which changes what lands in the
#                     working tree and therefore what gets built, so a by-name
#                     rule would silently make a live file inert. Same trade as
#                     the workflow enumeration above, and the same upkeep: after
#                     adding or moving one, re-check with
#                     `git ls-files '*.gitattributes' '.gitattributes'` and
#                     confirm the contents are still only linguist markers. A
#                     .gitattributes NOT on this list is classified by whatever
#                     rule its path falls under — the graph, or the unclassified
#                     fall-through — both of which fail safe
inert_config_pattern='^(examples/|\.github/|\.claude/|\.gemini/|img/|hack/testdata/|packages/tests/|hack/[^/]+\.bats$|hack/boilerplate\.go\.txt$|hack/dcgm-default-counters\.csv$|LICENSE$|\.gitignore$|\.pre-commit-config\.yaml$|\.coderabbit\.yaml$|packages/system/\.gitattributes$|packages/system/(backup-controller|backupstrategy-controller)/definitions/\.gitattributes$)'

# All known Chainsaw suites: every dir under hack/e2e-chainsaw/ holding a
# chainsaw-test.yaml (this excludes _lib/ and the top-level config files).
#
# Captured before the sed/sort rather than piped straight into them: a pipeline
# carries its LAST command's status, so `$(find ... | sed | sort)` would report
# sort's success whatever find did — the same blindness handled for yq below,
# and worse here, because this list is what every escalation prints. Errors go
# to stderr rather than /dev/null for the same reason.
if ! chainsaw_tests=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml); then
  echo "select-e2e: find failed listing the Chainsaw suites under hack/e2e-chainsaw — nothing can be decided without that list" >&2
  exit 1
fi
all_apps=$(printf '%s\n' "$chainsaw_tests" \
  | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort)

# An empty list here is a broken enumeration — a moved directory, a wrong
# working directory — not a project without tests, and it silently corrupts
# every answer the script can give:
#
#   - the three escalation branches print this list, so "run everything"
#     becomes a blank line, which both lanes read as "skip Chainsaw" before
#     posting the required "E2E Tests" status green;
#   - the examples/backups/<app>/ rule takes no escalation and still consults
#     it, as a membership test. With the list empty the test fails, the app is
#     not selected, and an edit to a backup harness that should run its suite
#     reports nothing to run instead.
#
# Neither is distinguishable in the output from a legitimately empty selection,
# which is the failure #3392 exists to keep out. Checking once here rather than
# at the escalation branches covers both, and covers the next consumer that
# reads the list without escalating.
#
# Both lanes run this step under `bash -e`, so the non-zero exit fails the job
# instead of falling through to the empty selection.
if [ -z "$all_apps" ]; then
  echo "select-e2e: found no chainsaw-test.yaml under hack/e2e-chainsaw — the suite enumeration is broken (wrong working directory?), refusing to decide anything from an empty suite list" >&2
  exit 1
fi

# Single exit point for the three escalating branches, so the invariant above
# has one consumer to reason about rather than three copies.
escalate_to_full_suite() {
  echo "$all_apps" | paste -sd ' ' -
  exit 0
}

# PackageSource name -> Chainsaw suite name(s). Most *-application sources map
# by stripping the suffix, a source that is not an app carries its own name
# (cozystack.kuberture owns the kuberture suite), and the few that match neither
# are listed explicitly.
#
# This is the inverse of select-install.sh's suite_to_source() and has to stay
# in step with it — a suite that does not round-trip is unreachable from its own
# package, so every change to that package escalates to the full run instead of
# selecting the one suite that covers it (#3665). The round-trip is pinned by a
# test rather than left to review.
#
# Mapping optimistically is safe: a name that is not a suite is dropped by
# intersect_suites() downstream.
#
# Only names the two general arms get wrong are listed. `postgres-application`
# and `external-dns` used to sit here and no longer do: the first is what
# stripping the suffix already produces, and the second is what carrying the
# source name through already produces, so both said the rule twice and made
# this list read as longer than the set of real exceptions.
src_to_suites() {
  case "$1" in
    vm-instance-application) echo vminstance ;;
    # The vminstance suite creates a VMDisk and asserts the DataVolume behind it
    # (hack/e2e-chainsaw/vminstance/vmdisk.yaml, vmdisk-vmi.yaml), so it is the
    # suite that covers this source and everything reaching it, kubevirt-cdi
    # included. Without the entry the source maps to a `vm-disk` suite that does
    # not exist, is dropped by intersect_suites(), and a CDI change reaches
    # nothing runnable through it.
    vm-disk-application) echo vminstance ;;
    kubernetes-application) echo "kubernetes-latest kubernetes-previous kubernetes-oidc-system kubernetes-oidc-customconfig" ;;
    securitygroup-controller) echo securitygroup ;;
    *-application) echo "${1%-application}" ;;
    *) echo "$1" ;;
  esac
}

# yq: path -> PackageSource name
build_owners_index() {
  yq -rN '.metadata.name as $n | .spec.variants[]?.components[]?.path | select(. != null and . != "") | . + "\t" + $n' "$SOURCES_DIR"/*.yaml
}

# yq: PackageSource name -> sources that depend on it (reverse of dependsOn)
#
# Exclude cozystack.cozystack-engine as a propagation hub. Every *-application
# source declares `dependsOn: cozystack.cozystack-engine` purely as an INSTALL
# ORDERING edge — the app's *-rd HelmRelease must wait for the engine to
# register the ApplicationDefinition CRD before it can reconcile. That is a
# universal lifecycle dependency, NOT a behavioral one: a change to an engine
# dependency (postgres-operator, keycloak, cert-manager, ...) does not alter any
# app's runtime behavior, so it must not fan test selection out to every app.
# Without this filter, postgres-operator -> keycloak -> cozystack-engine -> EVERY
# app, defeating test-impact analysis. Dropping the engine reverse edges keeps
# the engine reachable but stops it propagating selection downstream. A change to
# the engine itself still triggers the full suite: its group reaches no suite, so
# the per-path escalation below fires on it.
#
# cozystack.cozystack-basics is the second hub of that kind and is excluded for
# the same reason. Every edge into it exists so a namespace or a platform-wide
# policy is in place before the dependent installs — kubevirt-cdi says so in its
# own source ("Depend on cozystack-basics so the target namespace exists
# first") — which is install ordering, not behavior. It is also the hub where
# treating the edge as behavioral does the most damage, and in the wrong
# direction: cozystack-basics reaches no suite today, so it escalates, but it
# sits upstream of kubevirt-cdi, so the FIRST suite that lands downstream of CDI
# silently converts the platform's namespace-and-policy package from the full
# run to that one suite. Reproduced on #3426, where enabling the site-router
# suite takes cozystack-basics from 22 suites to `site-router` alone. Dropping
# the reverse edges keeps the package reachable and stops it propagating
# selection downstream, and a change to it still runs everything through the
# per-path escalation.
build_reverse_deps() {
  yq -rN '.metadata.name as $n | .spec.variants[]?.dependsOn[]? | select(. != null and . != "" and . != "cozystack.cozystack-engine" and . != "cozystack.cozystack-basics") | . + "\t" + $n' "$SOURCES_DIR"/*.yaml
}

# Each index is one yq, and `OWNERS=$(build_owners_index | sort -u)` reports
# sort's status, never yq's — so `set -eu` above cannot see a yq that failed.
# The index then comes back empty, every path falls through to escalation, and
# the run is a full suite indistinguishable in the output from "no owners
# matched". Safe in direction, invisible in nature: selection has stopped
# working and it reads as the conservatism it is supposed to be.
#
# So capture each half on its own, check its status, and say once, loudly, that
# yq is the broken component before taking the same fallback the empty index
# would have taken anyway.
broken=''
OWNERS=$(build_owners_index) || broken='the ownership index'
REVERSE=$(build_reverse_deps) || broken="${broken:+$broken and }the reverse-dependency index"
if [ -n "$broken" ]; then
  echo "select-e2e: yq failed building $broken from $SOURCES_DIR/*.yaml — the dependency graph is unusable, running the full suite" >&2
  escalate_to_full_suite
fi
# Sorted after the check rather than inside the pipeline above, for the reason
# the pipeline was the bug.
OWNERS=$(printf '%s\n' "$OWNERS" | sort -u)
REVERSE=$(printf '%s\n' "$REVERSE" | sort -u)

trigger_full=0
trigger_any=0
selected_groups=""
selected_apps=""

# `|| [ -n "$file" ]` is what makes the classification below total. POSIX read
# assigns the last line and then returns non-zero when the input does not end in
# a newline, so a plain `while read` silently drops it. That is the one way a
# path can reach this loop and be classified by nothing at all: the fall-through
# at the end, which exists so an unrecognised path escalates instead of
# selecting nothing, sits inside the body the drop skips. Callers building the
# list themselves rather than piping `git diff` are the ones that hit it, and
# the guard belongs here rather than in each of them.
while IFS= read -r file || [ -n "$file" ]; do
  [ -z "$file" ] && continue

  # 1. Skip: docs, dashboards, *.md. Checked before everything else because
  #    these can never matter wherever they live — a README inside
  #    packages/core/ must not escalate the way its templates do.
  if echo "$file" | grep -qE '^(docs/|dashboards/)' || echo "$file" | grep -qE '\.md$'; then
    continue
  fi

  # 2. Chainsaw edits. A per-suite file selects only that suite; the shared
  #    _lib helpers and the .chainsaw.yaml config affect every suite, so they
  #    escalate to the full run.
  case "$file" in
    hack/e2e-chainsaw/_lib/*|hack/e2e-chainsaw/.chainsaw.yaml)
      echo "select-e2e: '$file' is shared by every Chainsaw suite — escalating to the full suite" >&2
      trigger_full=1
      continue ;;
    hack/e2e-chainsaw/*/*.disabled)
      # A suite parked as chainsaw-test.yaml.disabled is registered nowhere and
      # executed by nothing, so an edit to it cannot regress a test. Matched
      # before the per-suite rule below, which reads the suite name off the
      # directory and ignores the suffix: the name it derives matches no
      # existing suite, the intersection at the bottom empties, and the
      # safety net for a genuinely unclassified selection escalates to the
      # full run — the most expensive outcome, bought by the one file class
      # that provably cannot affect anything. Inert is the classification;
      # the safety net keeps its job for paths no rule has looked at.
      continue ;;
    hack/e2e-chainsaw/*/*)
      app=$(echo "$file" | sed -nE 's,^hack/e2e-chainsaw/([^/]+)/.*,\1,p')
      selected_apps="$selected_apps $app"
      trigger_any=1
      continue ;;
    hack/e2e-apps/*)
      # The pre-Chainsaw per-app BATS suites. One file per app, named after it,
      # so the suite name comes off the basename exactly as the rule above takes
      # it off the directory.
      #
      # Deliberately NOT marked inert, even though what remains here is wired to
      # nothing after the Chainsaw migration: inert would bake that orphan status
      # into the rule and go quietly wrong the day a lane runs these again. The
      # mapping is correct either way — a file named after an app selects that
      # app's suite whether or not anything currently executes it.
      #
      # The membership test is what makes this per-path, and it is not optional.
      # Adding an unmatched name to selected_apps and letting the final
      # intersection drop it leaves the verdict to the REST of the diff: alone it
      # empties the selection and the backstop escalates, but beside any path that
      # contributed a suite the escalation vanishes and the run narrows to that
      # suite with nothing on stderr. That is the merge-before-escalate shape
      # #3330 removed from the graph walk, and it applies here for the same
      # reason — coverage is a property of the changed path.
      #
      # `[^/]+` rather than `.+`: POSIX case matches `/` with `*`, so this arm
      # also sees nested paths, and an unanchored capture would turn
      # hack/e2e-apps/fixtures/postgres.bats into the "suite" fixtures/postgres.
      # Nothing nested exists here today; whatever appears would be shared
      # material for these suites the way hack/e2e-chainsaw/_lib/ is for the
      # Chainsaw ones, so it escalates for the same reason rather than being
      # guessed at. A non-.bats file directly in the directory lands here too and
      # is treated the same way.
      app=$(echo "$file" | sed -nE 's,^hack/e2e-apps/([^/]+)\.bats$,\1,p')
      if [ -z "$app" ]; then
        echo "select-e2e: '$file' is not a per-app suite file (hack/e2e-apps/<name>.bats), so it is shared material for all of them — escalating to the full suite" >&2
        trigger_full=1
      elif echo "$all_apps" | grep -Fxq "$app"; then
        selected_apps="$selected_apps $app"
        trigger_any=1
      else
        echo "select-e2e: '$file' names no runnable suite ('$app') — escalating to the full suite" >&2
        trigger_full=1
      fi
      continue ;;
    examples/backups/*/*)
      # The etcd, postgres, mariadb and clickhouse backup round-trip tests
      # execute the example scripts under examples/backups/<app>/ as their
      # harness, so an edit there must run that app's suite. This mapping is
      # also why a round-trip Test belongs in its app's own suite dir: put it
      # in a dir whose name does not match examples/backups/<app>/ and an edit
      # to the harness selects a suite that cannot exercise it. A dir with no
      # matching suite is a docs-only demo and stays ignored (adding it to
      # selected_apps would empty the final intersection and trip the
      # full-suite safety net).
      app=$(echo "$file" | sed -nE 's,^examples/backups/([^/]+)/.*,\1,p')
      if echo "$all_apps" | grep -Fxq "$app"; then
        selected_apps="$selected_apps $app"
        trigger_any=1
      fi
      continue ;;
  esac

  # 3. Full-suite trigger
  if echo "$file" | grep -qE "$full_suite_pattern"; then
    echo "select-e2e: '$file' cannot be scoped to one suite (full_suite_pattern) — escalating to the full suite" >&2
    trigger_full=1
    continue
  fi

  # 4. Explicitly inert: repo meta, agent config, non-e2e workflows, fixtures.
  if echo "$file" | grep -qE "$inert_config_pattern"; then
    continue
  fi

  # 5. Component change: lookup in PackageSource graph
  rel=$(echo "$file" | sed -nE 's,^packages/(apps|system|extra)/([^/]+)/.*,\1/\2,p')
  if [ -n "$rel" ]; then
    src=$(echo "$OWNERS" | awk -v p="$rel" -F'\t' '$1==p {print $2}')
    if [ -n "$src" ]; then
      # Keep this path's owning sources together as one comma-joined group. The
      # unit coverage is decided over is the changed PATH, not the single
      # source: system/postgres-operator belongs to two PackageSources, one of
      # which reaches no suite, and deciding per source would escalate on that
      # half and run everything for every change to it.
      selected_groups="$selected_groups $(echo "$src" | paste -sd , -)"
      trigger_any=1
    else
      # Inside packages/ but no graph entry — be conservative.
      echo "select-e2e: no PackageSource in $SOURCES_DIR lists '$rel' as a component path — escalating to the full suite" >&2
      trigger_full=1
    fi
    continue
  fi

  # 6. Unclassified. Escalate instead of ignoring: an empty selection makes both
  #    e2e lanes skip Chainsaw and post "E2E Tests" green, so a path nobody has
  #    classified must fail safe, not fail open (#3392). Add genuinely inert
  #    paths to inert_config_pattern rather than relaxing this.
  echo "select-e2e: unclassified path '$file' — escalating to the full suite (classify it in hack/select-e2e.sh, see #3392)" >&2
  trigger_full=1
done < "$CHANGED"

if [ "$trigger_full" = 1 ]; then
  escalate_to_full_suite
fi

if [ "$trigger_any" = 0 ]; then
  exit 0  # nothing to run
fi

# Deduplicate a space-separated suite list; keep only names that are suites.
intersect_suites() {
  echo "$1" | tr ' ' '\n' | sort -u | grep -v '^$' | while read -r app; do
    if echo "$all_apps" | grep -Fxq "$app"; then
      echo "$app"
    fi
  done | paste -sd ' ' -
}

# Transitive closure over reverse-deps from the given PackageSource names, every
# reached source mapped through src_to_suites. Emits candidate suite names, not
# yet filtered — pass the result through intersect_suites.
#
# POSIX sh has no `local`, so this scribbles on all_sources/new/final. Its one
# call site invokes it inside $( ), which contains the damage; a direct call
# would clobber the caller's variables. A test pins that there is still exactly
# one call.
resolve_suites() {
  all_sources="$*"
  while :; do
    new=""
    for s in $all_sources; do
      deps=$(echo "$REVERSE" | awk -v src="$s" -F'\t' '$1==src {print $2}')
      for d in $deps; do
        case " $all_sources " in *" $d "*) ;; *) new="$new $d";; esac
      done
    done
    [ -z "$new" ] && break
    all_sources="$all_sources $new"
  done

  final=""
  for s in $all_sources; do
    final="$final $(src_to_suites "${s#cozystack.}")"
  done
  echo "$final"
}

# Escalation is a property of the changed path, not of the whole diff. A path
# that reaches no runnable suite is covered by nothing and forces the full run
# on its own account, whatever the rest of the diff selected.
#
# Reading that off an empty FINAL selection instead — which is what the old
# safety net at the bottom did, and still does as a backstop — let any other
# changed path that contributed a suite name swallow the escalation, with
# nothing in the output to record that it happened (#3330). The shape that hits
# it is ordinary rather than exotic: change a platform component, adjust the
# Chainsaw test next to it, and the component's escalation disappears behind
# that one suite.
group_suites=""
for g in $(echo "$selected_groups" | tr ' ' '\n' | sort -u); do
  s=$(intersect_suites "$(resolve_suites "$(echo "$g" | tr ',' ' ')")")
  if [ -z "$s" ]; then
    # Say which changed path bought the full run. Every other escalation here
    # names its cause on stderr, and this one is the common case rather than the
    # rare one — most packages reach no suite of their own — so without a line
    # "why did this pull request run everything" has no answer in the log. The
    # sources are printed rather than the path because they are what the walk
    # started from; the path that owns them is the one this group came from in
    # the classification loop above.
    echo "select-e2e: no runnable suite covers $(echo "$g" | tr ',' ' ') — escalating to the full suite" >&2
    escalate_to_full_suite
  fi
  group_suites="$group_suites $s"
done

# Union of the graph-selected suites and the directly-selected ones from
# per-suite edits. Walking the graph once per group rather than once over all of
# them costs an extra walk per changed path and buys the escalation above; the
# answer is unchanged, because reverse reachability distributes over union, so
# the union of the per-group results is the set the single walk produced.
final_apps=$(intersect_suites "$group_suites $selected_apps")

# Backstop. Every graph path above either escalates or contributes a suite that
# exists, and both rules that select a suite by name — the per-app BATS one and
# the examples/backups/<app>/ one — membership-test it first, so what still
# reaches this is a per-suite Chainsaw edit naming a directory that holds no
# chainsaw-test.yaml: shared material beside _lib/, or a suite nested deeper than
# the depth-2 scan looks. Selecting nothing for those would skip E2E outright, so
# failing towards the full suite is the only safe way to be wrong here.
#
# group_suites is empty whenever this fires — every group either escalated above
# or contributed a suite that exists — so the names worth naming are the
# directly-selected ones, and printing them says which directory or file the
# selector could not resolve.
#
# Deduplicated in the shell rather than through `tr | sort -u | grep -v | paste`,
# which is the same blindness this file fixes twice above: a pipeline reports its
# LAST command's status, so a failing tr or sort would be invisible under set -e
# and the message would name a partial list or nothing at all. That is only a
# cosmetic loss — the escalation itself is already decided — but the reason line
# is a contract now, and a contract that degrades silently is the thing this
# change set exists to remove. Unquoted expansion is the split, `case` is the
# membership test, and neither can half-succeed; it is also the idiom
# resolve_suites already uses.
if [ -z "$final_apps" ]; then
  unmatched=''
  for a in $selected_apps; do
    case " $unmatched " in
      *" $a "*) ;;
      *) unmatched="${unmatched:+$unmatched }$a" ;;
    esac
  done
  echo "select-e2e: no runnable suite is named by '$unmatched' — escalating to the full suite" >&2
  escalate_to_full_suite
fi

echo "$final_apps"
