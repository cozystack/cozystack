#!/bin/sh
# Read a list of changed files (one per line) and emit space-separated suite
# names whose Chainsaw suites under hack/e2e-chainsaw/ should run.
#
# Usage: hack/select-e2e.sh <changed-files> [<sources-dir>]
# Defaults: sources-dir = packages/core/platform/sources
#
# Output:
#   - empty         no E2E impact (docs / dashboards / *.md only)
#   - <suite names> selected per the PackageSource dependency graph
#   - full list     any path that affects all tests, OR an unrecognised
#                   packages/* path, OR a changed package that reaches no
#                   Chainsaw suite at all (conservative fallback)
set -eu

CHANGED="${1:?missing changed-files arg}"
SOURCES_DIR="${2:-packages/core/platform/sources}"

# Anything matching this pattern triggers the full Chainsaw suite. Per-suite
# edits (hack/e2e-chainsaw/<name>/...) are matched BEFORE this so editing one
# suite doesn't escalate to the full suite; the shared _lib helpers and the
# .chainsaw.yaml config affect every suite and DO escalate (handled inline).
full_suite_pattern='^(packages/library/|packages/core/|api/|cmd/|internal/|hack/[^/]+\.sh$|hack/[^/]+\.bats$|Makefile$|\.github/workflows/pull-requests\.yaml$)'

# All known Chainsaw suites: every dir under hack/e2e-chainsaw/ holding a
# chainsaw-test.yaml (this excludes _lib/ and the top-level config files).
all_apps=$(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml 2>/dev/null \
  | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,' | sort)

# Every escalation below prints $all_apps, and CI reads an empty selection as
# "skip E2E entirely" — so an empty discovery would turn each fail-safe into
# its opposite and disable E2E for the whole repo behind a green run. Refuse
# instead: at this point a missing suite tree means the wrong directory, not
# a repository with no tests.
if [ -z "$all_apps" ]; then
  echo "select-e2e: no Chainsaw suites under hack/e2e-chainsaw (wrong working directory?)" >&2
  exit 1
fi

# PackageSource name -> Chainsaw suite name(s). Most *-application sources map
# by stripping the suffix, and a source that is not an app carries its own name
# (cozystack.kuberture owns the kuberture suite); explicit overrides for the few
# that match neither. The inverse of select-install.sh's suite_to_source(), and
# kept in lockstep with it. Names that are not suites are dropped downstream by
# intersect_suites(), so this may map optimistically.
src_to_suites() {
  case "$1" in
    vm-instance-application) echo vminstance ;;
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
# the engine itself still triggers the full suite via the no-runnable-suite
# escalation below.
build_reverse_deps() {
  yq -rN '.metadata.name as $n | .spec.variants[]?.dependsOn[]? | select(. != null and . != "" and . != "cozystack.cozystack-engine") | . + "\t" + $n' "$SOURCES_DIR"/*.yaml
}

OWNERS=$(build_owners_index | sort -u)
REVERSE=$(build_reverse_deps | sort -u)

# The pipe above hands back sort's status, not yq's, so a broken sources dir
# yields an empty index and exits 0. Every packages/ path would then escalate:
# safe by luck, and it hides the real fault. Fail loudly instead.
if [ -z "$OWNERS" ]; then
  echo "select-e2e: no component paths found in $SOURCES_DIR" >&2
  exit 1
fi

trigger_full=0
trigger_any=0
selected_groups=""
selected_apps=""

while IFS= read -r file; do
  [ -z "$file" ] && continue

  # 1. Skip: docs, dashboards, *.md
  if echo "$file" | grep -qE '^(docs/|dashboards/)' || echo "$file" | grep -qE '\.md$'; then
    continue
  fi

  # 2. Chainsaw edits. A per-suite file selects only that suite; the shared
  #    _lib helpers and the .chainsaw.yaml config affect every suite, so they
  #    escalate to the full run.
  case "$file" in
    hack/e2e-chainsaw/_lib/*|hack/e2e-chainsaw/.chainsaw.yaml)
      trigger_full=1
      continue ;;
    hack/e2e-chainsaw/*/*)
      app=$(echo "$file" | sed -nE 's,^hack/e2e-chainsaw/([^/]+)/.*,\1,p')
      if echo "$all_apps" | grep -Fxq "$app"; then
        selected_apps="$selected_apps $app"
        trigger_any=1
      elif [ -f "hack/e2e-chainsaw/$app/chainsaw-test.yaml.disabled" ]; then
        # A switched-off suite (backup/): nothing under it can change what the
        # run tests, so it is ignored, the same way examples/backups/ below
        # ignores a name with no suite behind it. Escalating instead would run
        # every suite for an edit to a test that does not execute.
        :
      else
        # Anything else here is shared material the depth-2 scan cannot see —
        # a fixtures directory alongside _lib/, or a suite nested deeper than
        # all_apps looks. Chainsaw would run it in full mode, so escalate
        # rather than let the edit select nothing.
        trigger_full=1
      fi
      continue ;;
    examples/backups/*/*)
      # The etcd and postgres backup round-trip tests execute the example
      # scripts under examples/backups/<app>/ as their harness, so an edit
      # there must run that app's suite. A dir with no matching suite is a
      # docs-only demo and stays ignored.
      app=$(echo "$file" | sed -nE 's,^examples/backups/([^/]+)/.*,\1,p')
      if echo "$all_apps" | grep -Fxq "$app"; then
        selected_apps="$selected_apps $app"
        trigger_any=1
      fi
      continue ;;
  esac

  # 3. Full-suite trigger
  if echo "$file" | grep -qE "$full_suite_pattern"; then
    trigger_full=1
    continue
  fi

  # 4. Component change: lookup in PackageSource graph
  rel=$(echo "$file" | sed -nE 's,^packages/(apps|system|extra)/([^/]+)/.*,\1/\2,p')
  if [ -n "$rel" ]; then
    src=$(echo "$OWNERS" | awk -v p="$rel" -F'\t' '$1==p {print $2}')
    if [ -n "$src" ]; then
      # Keep this path's owning sources together as one comma-joined group, so
      # coverage can be decided per changed path further down.
      selected_groups="$selected_groups $(echo "$src" | paste -sd , -)"
      trigger_any=1
    else
      # Inside packages/ but no graph entry — be conservative.
      trigger_full=1
    fi
  fi
  # Anything else (e.g. unrelated workflow files, top-level configs) is
  # silently ignored.
done < "$CHANGED"

if [ "$trigger_full" = 1 ]; then
  echo "$all_apps" | paste -sd ' ' -
  exit 0
fi

if [ "$trigger_any" = 0 ]; then
  exit 0  # nothing to run
fi

# Deduplicate a space-separated suite list; keep only real Chainsaw suites.
intersect_suites() {
  echo "$1" | tr ' ' '\n' | sort -u | grep -v '^$' | while read -r app; do
    if echo "$all_apps" | grep -Fxq "$app"; then
      echo "$app"
    fi
  done | paste -sd ' ' -
}

# Transitive closure over reverse-deps from the given PackageSource names,
# every reached source mapped through src_to_suites. Emits candidate suite
# names, not yet filtered — pass the result through intersect_suites.
#
# POSIX sh has no `local`, so this scribbles on all_sources/new/final. Its one
# call site invokes it inside $( ), which contains the damage; a direct call
# would clobber the caller's variables.
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

# Escalation is a property of the changed path, not of the whole diff: a path
# that reaches no runnable suite is covered by nothing, and forces the full run
# on its own account. Reading that off an empty final selection instead would
# let anything else that contributes a suite name — an edit to one suite's
# Chainsaw test, say — swallow the escalation, with nothing in the output to
# record that it happened.
group_suites=""
for g in $(echo "$selected_groups" | tr ' ' '\n' | sort -u); do
  s=$(intersect_suites "$(resolve_suites "$(echo "$g" | tr ',' ' ')")")
  if [ -z "$s" ]; then
    echo "$all_apps" | paste -sd ' ' -
    exit 0
  fi
  group_suites="$group_suites $s"
done

# Union of the graph-selected suites and the directly-selected ones from
# per-suite edits. Reverse reachability distributes over union, so collecting
# each group's suites above is the same set a single closure over all of them
# would produce, one graph walk cheaper.
final_apps=$(intersect_suites "$group_suites $selected_apps")

# Backstop. Every branch above either escalates or contributes a suite that
# exists, so this should be unreachable; it stays because an empty selection
# makes CI skip the E2E step outright, and failing towards the full suite is
# the only safe way to be wrong here.
if [ -z "$final_apps" ]; then
  echo "$all_apps" | paste -sd ' ' -
  exit 0
fi

echo "$final_apps"
