#!/bin/sh
# Emit the JSON build matrix (the list of package dirs to build) for CI.
#
# Single source of truth: the `make -C packages/<dir> image` lines in the root
# Makefile `build:` target. Parsing them here keeps the CI matrix from drifting
# away from what `make build` actually builds.
#
# Usage: hack/build-matrix.sh [<changed-files>]
#   no arg / empty / "FULL" -> every build unit (full matrix)
#   <changed-files>         -> only the units whose package dir has a changed
#                              file, UNLESS a shared-dependency path changed, in
#                              which case the full matrix is emitted (a change to
#                              cozy-lib / the build macros / go.mod / the build
#                              workflows can affect any image, so rebuild all).
#
# Output: a JSON array of package dirs on stdout, e.g.
#   ["packages/apps/mariadb","packages/system/dashboard"]
# An empty selection prints `[]` -> the matrix produces zero build jobs.
set -eu

MAKEFILE="${MAKEFILE:-Makefile}"

# Paths that force a full rebuild when touched. Kept deliberately broad — a
# false full-rebuild only costs time, a missed dependent ships a stale image.
full_rebuild_pattern='^(packages/library/|hack/common-envs\.mk|hack/package\.mk|Makefile$|go\.mod$|go\.sum$|\.github/workflows/(pull-requests|build-main)\.yaml$|hack/build-matrix\.sh$)'

# The build units, parsed from the `build:` recipe (from `build:` to the next
# line that starts in column 0).
#
# packages/core/talos and packages/core/installer are deliberately excluded from
# the parallel matrix and handled by dedicated jobs instead:
#   - talos:     the nocloud disk image and the installer tarball are heavy and
#                shared through _out/assets; built once in an always-on leg
#                (e2e needs the disk on every non-docs PR regardless of scoping).
#   - installer: its `flux push artifact --path=packages` bundles the ENTIRE,
#                digest-patched packages tree into the OCI artifact the operator
#                pulls, so it must run in the finalize step AFTER every other
#                unit's digest edits are merged — never concurrently with them.
all_units() {
  sed -n '/^build:/,/^[^[:space:]]/p' "$MAKEFILE" \
    | grep -oE 'make -C packages/[A-Za-z0-9._/-]+ image' \
    | sed -E 's/^make -C (packages[^ ]+) image$/\1/' \
    | grep -vxE 'packages/core/(talos|installer)'
}

emit_json() {
  printf '['
  _first=1
  for _u in $1; do
    if [ "$_first" -eq 1 ]; then _first=0; else printf ','; fi
    printf '"%s"' "$_u"
  done
  printf ']\n'
}

units=$(all_units)

CHANGED="${1:-}"
if [ -z "$CHANGED" ] || [ "$CHANGED" = "FULL" ]; then
  emit_json "$units"
  exit 0
fi

if grep -qE "$full_rebuild_pattern" "$CHANGED"; then
  emit_json "$units"
  exit 0
fi

selected=""
for u in $units; do
  if grep -qE "^$u/" "$CHANGED"; then
    selected="$selected $u"
  fi
done

# Targeted cross-package fan-out that dir-scoping cannot see:
#   objectstorage-controller derives its COSI sidecar tag from
#   packages/system/seaweedfs/values.yaml, so a seaweedfs-only diff must still
#   rebuild objectstorage-controller to regenerate that tag.
if grep -qE '^packages/system/seaweedfs/' "$CHANGED"; then
  case " $selected " in
    *" packages/system/objectstorage-controller "*) : ;;
    *) selected="$selected packages/system/objectstorage-controller" ;;
  esac
fi

emit_json "$selected"
