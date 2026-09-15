#!/usr/bin/env bats
# Invariant for the packages artifact that packages/core/installer pushes:
#
# `flux push artifact` REPLACES its built-in ignore list when --ignore-paths is
# given rather than extending it, so the flag's value has to carry the defaults
# as well. Dropping one of them re-admits a file class the artifact never
# shipped before, and dropping node_modules/ re-admits the dependency tree that
# `pnpm install` leaves under images/console: a single file above Helm's 5MB
# per-file limit makes the dashboard chart unloadable on every cluster that
# pulls the bundle ("decompressed chart file ... is larger than the maximum file
# size 5242880"), with nothing in the build reporting it.
#
# Requires: make. flux is optional and only used to re-check the defaults.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
INSTALLER_MAKEFILE="$REPO_ROOT/packages/core/installer/Makefile"

# The upstream defaults, as printed by `flux push artifact --help`. The second
# test holds this copy against the installed flux so an upstream addition is
# noticed instead of being silently left out of the artifact's ignore list.
FLUX_DEFAULT_IGNORES=".git/,.gitignore,.gitmodules,.gitattributes,*.jpg,*.jpeg,*.gif,*.png,*.wmv,*.flv,*.tar.gz,*.zip"

ignore_paths_value() {
  sed -n 's/^PACKAGES_IGNORE_PATHS[[:space:]]*:*=[[:space:]]*//p' "$INSTALLER_MAKEFILE"
}

@test "the packages artifact ignores node_modules on top of flux's own defaults" {
  command -v sed >/dev/null || { echo "sed is required" >&2; exit 1; }

  value="$(ignore_paths_value)"
  [ -n "$value" ] # PACKAGES_IGNORE_PATHS must exist to be passed to flux

  # The recipe has to actually pass the variable, or the value is decoration.
  grep -q -- "--ignore-paths='\$(PACKAGES_IGNORE_PATHS)'" "$INSTALLER_MAKEFILE"

  printf '%s' ",$value," | grep -q ",node_modules/,"

  # Every upstream default survives the override.
  missing=""
  while IFS= read -r pattern; do
    printf '%s' ",$value," | grep -qF ",$pattern," || missing="$missing $pattern"
  done <<< "$(printf '%s' "$FLUX_DEFAULT_IGNORES" | tr ',' '\n')"
  [ -z "$missing" ] || { echo "ignore list dropped flux defaults:$missing" >&2; return 1; }
}

@test "the recorded flux defaults match the installed flux" {
  command -v flux >/dev/null || skip "flux not installed; the recorded defaults cannot be re-checked"

  # `--ignore-paths strings   set paths ... (default [a,b,c])`
  installed="$(flux push artifact --help 2>&1 \
    | sed -n 's/.*--ignore-paths .*(default \[\(.*\)\]).*/\1/p' \
    | head -1)"
  [ -n "$installed" ] # the help text must still advertise the defaults

  [ "$installed" = "$FLUX_DEFAULT_IGNORES" ] || {
    echo "flux defaults changed: $installed" >&2
    echo "update FLUX_DEFAULT_IGNORES and PACKAGES_IGNORE_PATHS together" >&2
    return 1
  }
}
