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
#                   packages/* path, OR a per-app source whose graph has
#                   no *-application descendants (conservative fallback)
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

# Suites present on disk but NOT YET runnable in CI: their prerequisites are not
# committed, so they cannot pass and must never be selected — not by TIA, not by
# a direct per-suite edit, and not by the full-suite escalation. The exclusion is
# applied to the FINAL output (strip_deferred), so it wins on every path while
# leaving the dependency-graph logic untouched. Run a deferred suite by hand:
#   make test-chainsaw CHAINSAW_SUITES="<name>"
#
# TODO: enable TIA selection of `site-router` once the VyOS golden image is
# published (uncomment the vyos-router entry in packages/system/vm-images) and
# the VyOS 1.5 firewall syntax is validated by the T13 empirical run — then
# delete it from DEFERRED_SUITES. The src_to_suites mapping below is already
# correct and needs no change at that point.
DEFERRED_SUITES="site-router"

# strip_deferred removes DEFERRED_SUITES entries from a space-separated suite
# list (mirrors the while-read idiom used for the final intersection below).
strip_deferred() {
  echo "$1" | tr ' ' '\n' | grep -v '^$' | while read -r s; do
    skip=0
    for d in $DEFERRED_SUITES; do
      [ "$s" = "$d" ] && skip=1
    done
    [ "$skip" = 0 ] && echo "$s"
  done | paste -sd ' ' -
}

# PackageSource name -> Chainsaw suite name(s). Most *-application sources map
# by stripping the suffix; explicit overrides for the few that don't.
src_to_suites() {
  case "$1" in
    postgres-application) echo postgres ;;
    vm-instance-application) echo vminstance ;;
    kubernetes-application) echo "kubernetes-latest kubernetes-previous kubernetes-oidc-system kubernetes-oidc-customconfig" ;;
    # The Go types module is `siterouter` (no hyphen, Go package rule) but the
    # Chainsaw suite dir is `site-router`. The suffix-strip default already
    # yields `site-router`; pin it explicitly so the mapping is documented and
    # stays correct if the default ever changes. NOTE: `site-router` is currently
    # in DEFERRED_SUITES (stripped from the final output) until its golden image
    # ships — this mapping is correct and simply has no CI effect until then.
    site-router-application) echo site-router ;;
    external-dns) echo external-dns ;;
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
# the engine itself still triggers the full suite via the no-app-descendants
# safety-net at the bottom of this script.
build_reverse_deps() {
  yq -rN '.metadata.name as $n | .spec.variants[]?.dependsOn[]? | select(. != null and . != "" and . != "cozystack.cozystack-engine") | . + "\t" + $n' "$SOURCES_DIR"/*.yaml
}

OWNERS=$(build_owners_index | sort -u)
REVERSE=$(build_reverse_deps | sort -u)

trigger_full=0
trigger_any=0
selected_sources=""
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
      selected_apps="$selected_apps $app"
      trigger_any=1
      continue ;;
    examples/backups/*/*)
      # The etcd and postgres backup round-trip tests execute the example
      # scripts under examples/backups/<app>/ as their harness, so an edit
      # there must run that app's suite. A dir with no matching suite is a
      # docs-only demo and stays ignored (adding it to selected_apps would
      # empty the final intersection and trip the full-suite safety net).
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
      selected_sources="$selected_sources $src"
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
  strip_deferred "$(echo "$all_apps" | paste -sd ' ' -)"
  exit 0
fi

if [ "$trigger_any" = 0 ]; then
  exit 0  # nothing to run
fi

# Transitive closure: walk reverse-deps from each selected source.
all_sources="$selected_sources"
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

# Filter to *-application sources, then map to Chainsaw suite names.
final=""
for s in $all_sources; do
  app=${s#cozystack.}
  case "$app" in
    *-application) final="$final $(src_to_suites "$app")" ;;
    external-dns) final="$final external-dns" ;;
  esac
done

# Add directly-selected suites from per-suite edits.
final="$final $selected_apps"

# Deduplicate; intersect with available Chainsaw suites.
final_apps=$(echo "$final" | tr ' ' '\n' | sort -u | grep -v '^$' | while read -r app; do
  if echo "$all_apps" | grep -Fxq "$app"; then
    echo "$app"
  fi
done | paste -sd ' ' -)

# Safety net: a system source with no *-application descendants would otherwise
# silently skip E2E. Fall back to full suite so a path inside the graph is
# never silently dropped.
if [ -z "$final_apps" ]; then
  strip_deferred "$(echo "$all_apps" | paste -sd ' ' -)"
  exit 0
fi

strip_deferred "$final_apps"
