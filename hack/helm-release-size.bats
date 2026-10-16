#!/usr/bin/env bats
# Helm keeps every release revision in one Secret, and the apiserver refuses a
# Secret over 1 MiB. A chart past that budget installs and then fails the one
# upgrade whose manifest is large enough ("Secret sh.helm.release.v1.<name>.vN
# is invalid: data: Too long"), and that release stays failed. The stored
# release carries the whole chart directory as it reaches helm-controller:
# the packages artifact applies no .helmignore, so tests/, images/ and the
# Makefile count too. hack/helm-release-size.py reproduces the encoding.
#
# The budget is 90 percent of the limit: the estimate runs about 1 percent
# above real Secrets, and the rest is room for a chart to grow by more than
# one change before anyone has to act.
#
# Harness compatibility note: CI runs this file under Bats through
# `make bats-unit-tests`. It also remains compatible with the narrower legacy
# `hack/cozytest.sh` translator, so it avoids `run`, `$status`, `$output`,
# `skip`, and setup()/teardown(). Paths stay repo-root-relative for both
# runners.
#
# Run with: bats hack/helm-release-size.bats

load test_helper

LIMIT=1048576
BUDGET=943718

@test "the estimator counts chart files the way Helm stores them" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/chart/templates" "$tmp/chart/tests"
  printf 'apiVersion: v2\nname: probe\nversion: 0.0.0\n' > "$tmp/chart/Chart.yaml"
  # Incompressible, so the stored size cannot be smaller than its base64.
  head -c 300000 /dev/urandom > "$tmp/chart/tests/blob"
  size=$(printf '' | python3 -I hack/helm-release-size.py "$tmp/chart")
  if [ "$size" -lt 400000 ]; then
    echo "estimate $size for a chart carrying 300000 random bytes; files are not counted" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "every chart's stored release fits the budget with its default render" {
  tmp=$(mktemp -d)
  : > "$tmp/over"
  n=0
  for c in packages/*/*/; do
    c=${c%/}
    [ -f "$c/Chart.yaml" ] || continue
    n=$((n + 1))
    # A chart that does not render offline still stores its files and
    # templates, so it is measured on those alone rather than skipped.
    helm template release "$c" -n default > "$tmp/manifest" 2>/dev/null || : > "$tmp/manifest"
    size=$(python3 -I hack/helm-release-size.py "$c" < "$tmp/manifest")
    if [ "$size" -gt "$BUDGET" ]; then
      echo "$c: $size bytes (budget $BUDGET, limit $LIMIT)" >> "$tmp/over"
    fi
  done
  if [ "$n" -lt 50 ]; then
    echo "only $n charts found under packages/; the sweep checked almost nothing" >&2
    false
  fi
  if [ -s "$tmp/over" ]; then
    echo "charts whose stored Helm release is over budget:" >&2
    cat "$tmp/over" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "backupstrategy-controller fits the budget on its largest revision" {
  # The default render gates every Strategy CR off. The revision that renders
  # all of them, with the Velero location on as in production, is the one
  # that grew past the limit once.
  c=packages/system/backupstrategy-controller
  size=$(helm template backupstrategy-controller "$c" -n cozy-backup-controller \
    --set backupStorage.bucketNameOverride=cozy-backups-0123 \
    --set velero.bslEnabled=true \
    | python3 -I hack/helm-release-size.py "$c")
  if [ "$size" -gt "$BUDGET" ]; then
    echo "$c: $size bytes with every default Strategy rendered (budget $BUDGET, limit $LIMIT)" >&2
    false
  fi
}
