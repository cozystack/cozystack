#!/usr/bin/env bats
# Asserts that cozystack-route-hostname-policy names every API version the
# vendored Gateway API CRD bundle actually serves for the route kind it gates.
#
# Why this needs a test rather than a comment. The policy lists API versions
# literally in matchConstraints, but the version set it must cover lives in a
# different package (packages/system/gateway-api-crds), is vendored from
# upstream, and moves whenever someone runs `make update` there. Nothing
# connects the two, so the list drifts silently in both directions.
#
# Both directions fail closed-looking and open-behaving. A version the bundle
# serves but the policy does not name is unguarded unless matchPolicy rescues
# it -- and matchPolicy (unset here, so Equivalent) only converts a request
# into a version the rule already names, so that rescue lasts exactly as long
# as at least one named version stays served. A version the policy names but
# the bundle drops takes the rescue away with it: once no named version is
# served the rule selects nothing at all, and failurePolicy: Fail does not
# fire, because nothing failed -- there was simply no match. A fail-closed
# tenant hostname control then stops applying with no error, no denial, and
# no log line. Upstream marks versions deprecated well before removing them,
# so the warning arrives in the bundle diff and nowhere else -- which is
# precisely the diff nobody reads line by line.
#
# This guard arrived with a Gateway API bundle bump, but the drift it caught
# was not caused by one: the tlsroutes rule had named a single version since
# the policy was written, while the bundle served three the whole time. A
# bump is simply when someone last looks at this file.
#
# The assertion is coverage, not equality: naming a version the bundle does
# not serve is inert (an unmatched resourceRules entry is not an error), so
# the policy may name more than is served, never fewer.
#
# The policy side is read from `helm template` output rather than the template
# source, so what is checked is the object the apiserver would receive.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs
# as a shell function under `set -eu -x`, so a non-zero exit is the failure.
# Paths are repo-root-relative: BATS_TEST_DIRNAME is unset and would abort the
# whole suite under `set -u`.
#
# Requires: yq (mikefarah v4+), helm. Both are available on the project's CI
# image and are already relied on by other hack/*.bats.
#
# Run with: hack/cozytest.sh hack/route-hostname-policy-version-coverage.bats

CRD_BUNDLE=packages/system/gateway-api-crds/templates/crds-experimental.yaml
BASICS_CHART=packages/system/cozystack-basics

served_versions() {
  yq eval-all \
    "select(.kind == \"CustomResourceDefinition\" and .metadata.name == \"$1\")
     | .spec.versions[] | select(.served == true) | .name" \
    "$CRD_BUNDLE" | sort -u
}

policy_versions() {
  helm template cozystack-basics "$BASICS_CHART" \
    --api-versions "admissionregistration.k8s.io/v1/ValidatingAdmissionPolicy" \
    | yq eval-all \
      "select(.kind == \"ValidatingAdmissionPolicy\" and .metadata.name == \"$1\")
       | .spec.matchConstraints.resourceRules[]
       | select(.resources[] == \"$2\") | .apiVersions[]" - | sort -u
}

# Fails on an empty set from either side: a yq expression that stops matching
# (renamed policy, restructured CRD) would otherwise report full coverage of
# nothing.
assert_covers() {
  crd_name=$1
  policy_name=$2
  resource=$3

  served=$(served_versions "$crd_name")
  named=$(policy_versions "$policy_name" "$resource")

  if [ -z "$served" ]; then
    echo "no served versions found for $crd_name in $CRD_BUNDLE" >&2
    exit 1
  fi
  if [ -z "$named" ]; then
    echo "no apiVersions found for $resource in $policy_name" >&2
    exit 1
  fi

  # Membership by loop rather than comm(1) with process substitution: the
  # cozytest.sh harness rejects <(...) with a syntax error, which exits
  # non-zero and would read as a genuine failure.
  missing=
  for version in $served; do
    if ! echo "$named" | grep -qx "$version"; then
      missing="$missing $version"
    fi
  done

  if [ -n "$missing" ]; then
    echo "$policy_name does not name every served version of $crd_name" >&2
    echo "served:  $(echo "$served" | tr '\n' ' ')" >&2
    echo "named:   $(echo "$named" | tr '\n' ' ')" >&2
    echo "missing: $(echo "$missing" | tr '\n' ' ')" >&2
    exit 1
  fi
}

@test "TLSRoute hostname policy names every served tlsroutes version" {
  assert_covers tlsroutes.gateway.networking.k8s.io \
    cozystack-route-hostname-policy-tls tlsroutes
}

@test "HTTPRoute hostname policy names every served httproutes version" {
  assert_covers httproutes.gateway.networking.k8s.io \
    cozystack-route-hostname-policy httproutes
}
