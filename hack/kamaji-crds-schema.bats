#!/usr/bin/env bats
# The kamaji package ships its CRDs from charts/kamaji-crds/templates/, which
# read their schema with .Files.Get from charts/kamaji-crds/hack/. Helm skips
# .helmignore when it reads a chart out of an archive and honours it when it
# reads one out of a directory, so a pattern matching that hack/ directory
# leaves the cluster with full schemas and every local render, lint and package
# with a CRD whose spec is empty. That split hides schema defects from every
# check that runs before a cluster sees the chart, which is all of ours.
#
# The package path is relative because both runners invoke these from the
# repository root, as the neighbouring render tests assume too.

@test "kamaji CRD templates render their schema" {
  rendered=$(helm template kamaji packages/system/kamaji --namespace cozy-kamaji)

  for crd in tenantcontrolplanes datastores kubeconfiggenerators; do
    versions=$(printf '%s\n' "$rendered" | yq -r "select(.metadata.name == \"$crd.kamaji.clastix.io\") | .spec.versions | length")
    [ "$versions" -gt 0 ]
  done

  spec_props=$(printf '%s\n' "$rendered" | yq -r 'select(.metadata.name == "tenantcontrolplanes.kamaji.clastix.io") | .spec.versions[0].schema.openAPIV3Schema.properties.spec.properties | length')
  [ "$spec_props" -gt 0 ]
}
