#!/usr/bin/env bats
# A strategy CRD that inlines the upstream pod schema adds about 600 KB, and
# backupstrategy-controller stores each CRD twice in every Helm release
# revision, inside one Secret capped at 1 MiB. Three of them did once, and the
# upgrade that renders the default strategies failed with "Too long". Pod
# templates in api/backups/strategy are therefore Schemaless with
# PreserveUnknownFields (see the package doc there). hack/helm-release-size.bats
# catches the total; this names the field that caused it.
#
# Harness compatibility note: CI runs this file under Bats through
# `make bats-unit-tests`. It also remains compatible with the narrower legacy
# `hack/cozytest.sh` translator, so it avoids `run`, `$status`, `$output`,
# `skip`, and setup()/teardown(). Paths stay repo-root-relative for both
# runners.
#
# Run with: bats hack/strategy-crd-schemaless.bats

load test_helper

DEFS=packages/system/backupstrategy-controller/definitions

@test "no strategy CRD inlines a pod schema" {
  tmp=$(mktemp -d)
  n=0
  for f in "$DEFS"/strategy.backups.cozystack.io_*.yaml; do
    n=$((n + 1))
    # A PodSpec is the object whose containers[] items carry an image field;
    # report the property path that holds it, so the message names the field.
    yq -N '[.. | select(tag == "!!map" and has("properties") and (.properties.containers.items.properties.image != null)) | path | join(".")] | .[]' "$f" \
      | sed "s#^#$(basename "$f"): #" >> "$tmp/inlined"
  done
  if [ "$n" -lt 10 ]; then
    echo "only $n strategy CRDs under $DEFS; nothing meaningful was checked" >&2
    false
  fi
  if [ -s "$tmp/inlined" ]; then
    echo "strategy CRDs inlining a pod schema; mark the Go field +kubebuilder:validation:Schemaless and +kubebuilder:pruning:PreserveUnknownFields:" >&2
    cat "$tmp/inlined" >&2
    false
  fi
  rm -rf "$tmp"
}
