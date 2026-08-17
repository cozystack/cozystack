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
       | select(.resources[] | select(. == \"$2\")) | .apiVersions[]" - | sort -u
}

# Echoes, space-prefixed, each served version the policy does not name. The
# comparison lives here rather than inline so the fixtures at the bottom can
# drive it with synthetic input; neutering it turns them red.
#
# Membership by loop rather than comm(1) with process substitution: the
# cozytest.sh harness rejects <(...) with a syntax error, which exits non-zero
# and would read as a genuine failure.
uncovered() {
  served=$1
  named=$2
  out=
  for version in $served; do
    if ! echo "$named" | grep -qxF "$version"; then
      out="$out $version"
    fi
  done
  echo "$out"
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

  missing=$(uncovered "$served" "$named")

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

# The two cases above only ever see a tree that is already correct, so on their
# own they cannot tell a working comparison from one that stopped comparing.
# These drive it with synthetic input, the way hack/bats-no-exit-trap.bats and
# hack/md-no-hardwrap.bats feed their checkers a broken sample and require a
# complaint.
#
# Both layers need their own fixture. Driving uncovered() alone leaves
# assert_covers unprotected -- emptying it to a no-op keeps every other case in
# this file green, because the live-tree cases are the only callers and the
# tree they read is correct. So the cases below feed assert_covers itself, in a
# subshell with the two readers redefined.
#
# Each requires the complaint it expects, not merely a non-zero exit. A renamed
# helper exits 127 and an unbound variable under `set -u` exits 1, and either
# would read as a successful rejection if only the status were checked -- red
# for the wrong reason is the same false comfort as no fixture at all.
assert_covers_rejects() {
  expected=$1
  rc=0
  out=$(
    served_versions() { printf '%s\n' "$FIXTURE_SERVED"; }
    policy_versions() { printf '%s\n' "$FIXTURE_NAMED"; }
    assert_covers fixture.crd fixture-policy fixtureroutes 2>&1
  ) || rc=$?
  [ "$rc" != "0" ]
  case "$out" in
    *"$expected"*) ;;
    *) echo "rejected, but not for the expected reason" >&2
       echo "wanted: $expected" >&2
       echo "got:    $out" >&2
       exit 1 ;;
  esac
}

@test "assert_covers rejects a policy that omits a served version" {
  FIXTURE_SERVED='v1
v1alpha2'
  FIXTURE_NAMED='v1alpha2'
  export FIXTURE_SERVED FIXTURE_NAMED
  assert_covers_rejects "does not name every served version"
}

@test "assert_covers rejects an empty served set rather than passing vacuously" {
  FIXTURE_SERVED=''
  FIXTURE_NAMED='v1'
  export FIXTURE_SERVED FIXTURE_NAMED
  assert_covers_rejects "no served versions found"
}

@test "assert_covers rejects an empty named set rather than passing vacuously" {
  FIXTURE_SERVED='v1'
  FIXTURE_NAMED=''
  export FIXTURE_SERVED FIXTURE_NAMED
  assert_covers_rejects "no apiVersions found"
}

@test "the coverage check names a served version the policy omits" {
  missing=$(uncovered 'v1
v1alpha2
v1alpha3' 'v1alpha2')
  [ "$missing" = " v1 v1alpha3" ]
}

@test "the coverage check stays silent when every served version is named" {
  missing=$(uncovered 'v1
v1beta1' 'v1
v1beta1')
  [ -z "$missing" ]
}
