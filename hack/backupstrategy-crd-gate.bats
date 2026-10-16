#!/usr/bin/env bats
# backupstrategy-controller ships its strategy.backups.cozystack.io CRDs as
# ordinary templates in the same release as the default Strategy CRs. Helm maps
# every document of a release through the RESTMapper before it applies any, so
# one Strategy CR whose CRD is not served yet fails the whole release with "no
# matches for kind" and applies nothing, the CRDs included. That happens on the
# upgrade that first ships a kind, and on the first render of an external-S3
# install, where the bucket gate is already open. Every default Strategy must
# therefore also gate on its CRD being established (the crdEstablished helper).
#
# helm-unittest can only pin the templates someone remembered to list. These
# cases discover the templates from the render itself, so a new
# strategy-*-default.yaml without the CRD gate fails here without anyone
# touching a test.
#
# Harness compatibility note: CI runs this file under Bats through
# `make bats-unit-tests`. It also remains compatible with the narrower legacy
# `hack/cozytest.sh` translator, so it avoids `run`, `$status`, `$output`,
# `skip`, and setup()/teardown(). Paths stay repo-root-relative for both
# runners.
#
# Run with: bats hack/backupstrategy-crd-gate.bats

load test_helper

CHART=packages/system/backupstrategy-controller
STRATEGY_API='strategy.backups.cozystack.io/'

# Every Strategy CR in the render under $1, one "<template> <kind>/<name>" line
# each. --output-dir keeps one file per template, which is what names the
# offender.
strategy_crs() {
  find "$1" -name '*.yaml' | sort | while read -r f; do
    yq eval-all "select(.apiVersion // \"\" | test(\"^${STRATEGY_API}\")) | .kind + \"/\" + .metadata.name" "$f" \
      | sed "s#^#$(basename "$f") #"
  done
}

@test "no Strategy CR renders while no strategy CRD is established, for any template" {
  tmp=$(mktemp -d)

  # provisionBucket=false resolves the bucket name from values, so every bucket
  # gate is open, while the render has no apiserver and every CRD lookup
  # answers "absent": the state of an upgrade that ships a new kind, and of the
  # first external-S3 install.
  helm template t "$CHART" -n cozy-backup-controller \
    --set backupStorage.provisionBucket=false \
    --set backupStorage.bucketName=ext-bucket \
    --output-dir "$tmp/gated" >/dev/null
  strategy_crs "$tmp/gated" > "$tmp/gated.txt"

  # Positive control: the same render with only the CRD gate opened (the
  # offline bucketNameOverride short-circuits it) must produce Strategy CRs,
  # or the assertion above it would hold for an empty render.
  helm template t "$CHART" -n cozy-backup-controller \
    --set backupStorage.provisionBucket=false \
    --set backupStorage.bucketName=ext-bucket \
    --set backupStorage.bucketNameOverride=ext-bucket \
    --output-dir "$tmp/open" >/dev/null
  strategy_crs "$tmp/open" > "$tmp/open.txt"

  if [ -s "$tmp/gated.txt" ]; then
    echo "Strategy CRs rendered without an established CRD (missing crdEstablished gate):" >&2
    cat "$tmp/gated.txt" >&2
    false
  fi
  if [ ! -s "$tmp/open.txt" ]; then
    echo "control render produced no Strategy CR; the check above proves nothing" >&2
    false
  fi
  # A strategy template that renders nothing by default (behind an opt-in
  # value) would pass the first check without its gate ever being exercised.
  # Every one of them has to show up in the control render; one that is off by
  # default needs its switch added to both renders above.
  missing=""
  for f in "$CHART"/templates/strategy-*.yaml; do
    f=$(basename "$f")
    if ! grep -q "^$f " "$tmp/open.txt"; then
      missing="$missing $f"
    fi
  done
  if [ -n "$missing" ]; then
    echo "strategy templates absent from the control render, so unchecked:$missing" >&2
    false
  fi

  rm -rf "$tmp"
}

@test "every rendered Strategy CR gates on the CRD this chart ships for its kind" {
  tmp=$(mktemp -d)

  # kind -> CRD name, from the CRDs this chart actually renders.
  for f in "$CHART"/definitions/*.yaml; do
    yq eval '.spec.names.kind + " " + .metadata.name' "$f"
  done > "$tmp/crds.txt"
  if [ ! -s "$tmp/crds.txt" ]; then
    echo "no CRDs read from $CHART/definitions" >&2
    false
  fi

  helm template t "$CHART" -n cozy-backup-controller \
    --set backupStorage.bucketNameOverride=ext-bucket \
    --output-dir "$tmp/open" >/dev/null
  strategy_crs "$tmp/open" > "$tmp/open.txt"
  if [ ! -s "$tmp/open.txt" ]; then
    echo "render produced no Strategy CR; nothing was checked" >&2
    false
  fi

  # The gate passes on any established CRD it is given, so a gate naming the
  # wrong CRD (a typo, a copied sibling) leaves the CR out of every live render
  # while the case above stays green. The name has to be the one serving the
  # kind.
  bad=""
  while read -r tpl obj; do
    kind=${obj%%/*}
    crd=$(awk -v k="$kind" '$1 == k { print $2 }' "$tmp/crds.txt")
    if [ -z "$crd" ]; then
      bad="$bad
$tpl: $obj has no CRD in $CHART/definitions"
      continue
    fi
    if ! grep -qF "\"crd\" \"$crd\"" "$CHART/templates/$tpl"; then
      bad="$bad
$tpl: $obj does not gate on crdEstablished for $crd"
    fi
  done < "$tmp/open.txt"
  if [ -n "$bad" ]; then
    echo "Strategy CR templates with a wrong or missing CRD gate:$bad" >&2
    false
  fi

  rm -rf "$tmp"
}
