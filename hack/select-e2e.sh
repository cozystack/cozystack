#!/bin/sh
# Read a list of changed files (one per line) and emit space-separated app
# names whose bats files in hack/e2e-apps/ should run.
#
# Usage: hack/select-e2e.sh <changed-files> [<sources-dir>]
# Defaults: sources-dir = packages/core/platform/sources
#
# Output:
#   - empty       no E2E impact (docs / dashboards / *.md only)
#   - <app names> selected per the PackageSource dependency graph
#   - full list   any path that affects all tests, OR an unrecognised
#                 packages/* path, OR a per-app source whose graph has
#                 no *-application descendants (conservative fallback)
set -eu

CHANGED="${1:?missing changed-files arg}"
SOURCES_DIR="${2:-packages/core/platform/sources}"

# Anything matching this pattern triggers the full bats suite. Per-app bats
# (hack/e2e-apps/<name>.bats) are matched BEFORE this so editing one bats
# file doesn't escalate to the full suite.
full_suite_pattern='^(packages/library/|packages/core/|api/|cmd/|internal/|hack/[^/]+\.sh$|hack/[^/]+\.bats$|hack/e2e-apps/[^/]+\.sh$|Makefile$|\.github/workflows/(pull-requests|release-e2e)\.yaml$)'

# All known per-app bats files
all_apps=$(ls hack/e2e-apps/*.bats 2>/dev/null | xargs -n1 basename | sed 's/\.bats$//')

# PackageSource name -> bats name(s). Most *-application sources map by
# stripping the suffix; explicit overrides for the few that don't.
app_to_bats() {
  case "$1" in
    postgres-application) echo postgres ;;
    vm-instance-application) echo vminstance ;;
    kubernetes-application) echo "kubernetes-latest kubernetes-previous" ;;
    external-dns) echo external-dns ;;
    *-application) echo "${1%-application}" ;;
    *) echo "$1" ;;
  esac
}

# yq: path -> PackageSource name
build_owners_index() {
  for f in "$SOURCES_DIR"/*.yaml; do
    src=$(yq -r '.metadata.name' "$f")
    yq -r '.spec.variants[]?.components[]?.path // ""' "$f" | while read -r path; do
      if [ -n "$path" ]; then echo "$path	$src"; fi
    done
  done
}

# yq: PackageSource name -> sources that depend on it (reverse of dependsOn)
build_reverse_deps() {
  for f in "$SOURCES_DIR"/*.yaml; do
    src=$(yq -r '.metadata.name' "$f")
    yq -r '.spec.variants[]?.dependsOn[]? // ""' "$f" | while read -r dep; do
      if [ -n "$dep" ]; then echo "$dep	$src"; fi
    done
  done
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

  # 2. Per-app bats first — editing one bats file selects only that app.
  if echo "$file" | grep -qE '^hack/e2e-apps/[^/]+\.bats$'; then
    app=$(basename "$file" .bats)
    selected_apps="$selected_apps $app"
    trigger_any=1
    continue
  fi

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
  echo "$all_apps" | tr '\n' ' '
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

# Filter to *-application sources, then map to bats names.
final=""
for s in $all_sources; do
  app=${s#cozystack.}
  case "$app" in
    *-application) final="$final $(app_to_bats "$app")" ;;
    external-dns) final="$final external-dns" ;;
  esac
done

# Add directly-selected apps from per-app bats edits.
final="$final $selected_apps"

# Deduplicate; intersect with available bats files.
final_apps=$(echo "$final" | tr ' ' '\n' | sort -u | grep -v '^$' | while read -r app; do
  if echo "$all_apps" | grep -qw "$app"; then
    echo "$app"
  fi
done | paste -sd ' ' -)

# Safety net: a system source with no *-application descendants would otherwise
# silently skip E2E. Fall back to full suite so a path inside the graph is
# never silently dropped.
if [ -z "$final_apps" ]; then
  echo "$all_apps" | tr '\n' ' '
  exit 0
fi

echo "$final_apps"
