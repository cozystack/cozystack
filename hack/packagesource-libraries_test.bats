#!/usr/bin/env bats
# A chart reaches cozy-lib through a charts/cozy-lib symlink in the tree, but
# flux push artifact skips symlinks, so the packages artifact carries none. A
# component gets the library only from the libraries it declares in its
# PackageSource, which the operator copies into charts/ of its artifact. A
# component that vendors cozy-lib without declaring it renders in every test
# and fails on the cluster, and only where the template calling the helper is
# switched on. The same chart can be a component of several PackageSources, so
# every one of them must declare it.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "every PackageSource component whose chart vendors cozy-lib declares it" {
  local missing="" file variant component path libs vlibs
  for file in "$REPO_ROOT"/packages/core/platform/sources/*.yaml; do
    # '|' rather than a tab: read collapses adjacent whitespace separators,
    # which would shift the fields after an empty list.
    while IFS='|' read -r variant component path libs vlibs; do
      [ -e "$REPO_ROOT/packages/$path/charts/cozy-lib/Chart.yaml" ] || continue
      case ",$libs," in *,cozy-lib,*) ;; *) missing="$missing ${file##*/}:$variant/$component(component)" ;; esac
      case ",$vlibs," in *,cozy-lib,*) ;; *) missing="$missing ${file##*/}:$variant/$component(variant)" ;; esac
    done < <(yq '.spec.variants[] | ((.libraries // []) | map(.name) | join(",")) as $vlibs | .name as $v | .components[] | [$v, .name, .path, ((.libraries // []) | join(",")), $vlibs] | join("|")' "$file")
  done
  if [ -n "$missing" ]; then
    echo "components vendoring cozy-lib without declaring it:$missing" >&2
    return 1
  fi
}

# `make update` of a package that vendors an upstream chart typically starts
# with rm -rf charts, which removes the cozy-lib link along with it; nothing
# fails until the next render calls a helper that is no longer there.
@test "make update of a package that vendors cozy-lib puts the link back" {
  local missing="" link pkg recipe
  for link in "$REPO_ROOT"/packages/*/*/charts/cozy-lib; do
    [ -L "$link" ] || continue
    pkg="${link%/charts/cozy-lib}"
    [ -f "$pkg/Makefile" ] || continue
    recipe=$(sed -n '/^update:/,/^[^[:space:]]/p' "$pkg/Makefile")
    printf '%s\n' "$recipe" | grep -qE 'rm -rf charts|find charts' || continue
    printf '%s\n' "$recipe" | grep -qF 'ln -sfn ../../../library/cozy-lib charts/cozy-lib' ||
      missing="$missing ${pkg#"$REPO_ROOT"/}"
  done
  if [ -n "$missing" ]; then
    echo "update targets that delete charts/ without restoring cozy-lib:$missing" >&2
    return 1
  fi
}
