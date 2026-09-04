#!/bin/sh
# Render every app chart and fail if one does not produce valid YAML.
#
# The gap this closes: helm-unittest covers roughly a fifth of the packages, so
# for most charts nothing outside the E2E suite ever renders them. A template
# that breaks on its own defaults is then found by a 176-minute lane, or by a
# release, rather than by a check that costs a second.
#
# What it does NOT cover, so the number is not read as more than it is.
#
# `lookup` returns nothing here. Without a cluster helm gives an empty result and
# charts carry on -- which is why this works at all -- but the sweep then renders
# only the empty-result side. There are 40 template lookup calls under
# packages/apps/*/templates/ (`grep -rho 'lookup "'`; a plain `grep -c 'lookup '`
# returns 55 by counting prose in comments), and 13 of them sit inside a
# conditional on the result, so those 13 branches have a side this never renders.
# The unrendered side is where a whole class of real bugs has lived: the
# lookup-preserve pattern that keeps an immutable PVC storageClass across
# reconciles, and adopt-in-place migrations.
#
# That side is NOT out of reach, and it does not need a cluster: helm-unittest
# can fake the API a chart looks up, via `kubernetesProvider` in a test suite.
# packages/apps/clickhouse/tests/backup_api_password_persistence_test.yaml is
# the worked example -- it covers both the mint and the preserve branch of the
# backup API password, in milliseconds. So lookup branches belong there, per
# chart, where the fake can be specific about what exists; this sweep stays what
# it is, a check that every chart still renders at all.
#
# Do not read a green sweep as "these charts render correctly", only as "they
# render, under these cluster states, on the empty-lookup path".
#
# Values outside `_cluster`/`_namespace` are chart defaults. Presets, replica
# counts and feature toggles are not swept; assertions about what the output
# CONTAINS belong in helm-unittest, per package, where they can be specific.
#
# Usage: hack/check-render-matrix.sh [chart-dir ...]
#        defaults to packages/apps/*/
set -eu

# Cluster states to render each chart under. Each file reproduces the `_cluster`
# map the platform injects, taken from packages/core/platform/templates/apps.yaml
# rather than guessed. Every SCALAR there goes through `| quote`, so the booleans
# arrive as the strings "true"/"false" -- tenant compares one with
# `eq $oidcEnabled "true"` and a real bool makes helm fail on incompatible types.
# `scheduling` and `branding` are the exceptions: the platform emits those as
# maps. hack/testdata/render-fixtures/README.md carries the detail.
#
# More than one, and that is the difference between a check and a formality:
# charts branch on these values, so a single state renders one side of each
# branch and passes a chart whose other side is broken.
FIXTURE_DIR="hack/testdata/render-fixtures"

# Charts needing one more value of their own. Kept as a table rather than folded
# into FIXTURE so each entry names a chart-specific requirement instead of
# widening what every chart is rendered with.
# Empty today. Kept because the next chart that needs one value of its own
# should get it here rather than by widening FIXTURE for all of them.
extra_values() {
  case "$1" in
    *) echo "" ;;
  esac
}

# Charts that cannot be rendered without a live cluster, with the reason. A
# chart lands here only when the blocker is a lookup whose empty result the
# template turns into a hard failure -- not merely because it uses lookup, which
# most charts do and survive (helm returns an empty result and they carry on).
#
#   vm-instance, kubernetes-nodes
#                both resolve an instance type through a lookup of
#                VirtualMachineClusterInstancetype and fail when it finds
#                nothing ("specified instanceType u1.medium not found in
#                cluster"). That check is the point of those templates in
#                production, so the charts are skipped rather than weakened.
#
#                Worth being precise, because an earlier version of this comment
#                was not: for kubernetes-nodes that lookup is the THIRD blocker,
#                not the first. It also needs `--set cluster=<name>` and a
#                release name starting with `kubernetes-nodes-<cluster>-`, and
#                both of those are ordinary fixture work that extra_values() and
#                release_name() below exist for. Only after supplying them does
#                the chart reach the lookup. vm-instance likewise renders with
#                explicit resources.* values; the lookup is what stops it on
#                defaults. So "these need a cluster" is true of the last blocker
#                in each, not of the charts as a whole.
#
# Every skip costs coverage, so keep the list short and say why.
skip_reason() {
  case "$1" in
    vm-instance|kubernetes-nodes) echo "looks up VirtualMachineClusterInstancetype and hard-fails on an empty result" ;;
    *) echo "" ;;
  esac
}

# The release name matters to more than cosmetics: tenant's own template refuses
# a name that is not `tenant-<something>`, so a fixed name would fail it for a
# reason that has nothing to do with rendering health.
release_name() {
  case "$1" in
    tenant) echo "tenant-sub" ;;
    *) echo "$1-render-check" ;;
  esac
}

charts="$*"
if [ -z "$charts" ]; then
  charts=$(find packages/apps -mindepth 1 -maxdepth 1 -type d | sort)
fi
if [ -z "$charts" ]; then
  echo "check-render-matrix: found no charts under packages/apps — refusing to report success from an empty sweep" >&2
  exit 1
fi

# -maxdepth 1 so the guard tests exactly what the loop below globs; a recursive
# find here would pass on a fixture in a subdirectory the glob never reaches, and
# every chart would then fail on the literal `*.yaml`.
if [ -z "$(find "$FIXTURE_DIR" -maxdepth 1 -name '*.yaml' 2>/dev/null)" ]; then
  echo "check-render-matrix: no fixtures under $FIXTURE_DIR — every chart would render under nothing and the sweep would report success having checked no state" >&2
  exit 1
fi

rc=0
rendered=0
skipped=0
for dir in $charts; do
  name=$(basename "$dir")

  reason=$(skip_reason "$name")
  if [ -n "$reason" ]; then
    echo "SKIP $name ($reason)"
    skipped=$((skipped + 1))
    continue
  fi

  chart_failed=0
  for fixture in "$FIXTURE_DIR"/*.yaml; do
    state=$(basename "$fixture" .yaml)
    out=$(mktemp)
    # shellcheck disable=SC2086  # extra_values is an argument list
    if ! helm template "$(release_name "$name")" "$dir" -n tenant-test \
        -f "$fixture" $(extra_values "$name") >"$out" 2>"$out.err"; then
      echo "FAIL $name ($state)"
      grep -v 'level=INFO' "$out.err" | sed 's/^/    /' | head -12
      rc=1
      chart_failed=1
    fi
    rm -f "$out" "$out.err"
  done
  if [ "$chart_failed" -ne 0 ]; then
    continue
  fi

  rendered=$((rendered + 1))
done

echo "check-render-matrix: $rendered rendered, $skipped skipped"
exit "$rc"
