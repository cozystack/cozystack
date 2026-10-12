# shellcheck shell=sh
# Shared definitions for the stable-candidate packages artifact.
#
# Sourced by hack/promote-packages-artifact.sh, which publishes the candidate,
# and hack/verify-promoted-packages.sh, which proves it. The publisher's whole
# reason to inspect the pin at all is to make the verifier's refusal land at
# dispatch time, before a registry write, two commits and a draft release exist
# — so the two have to be testing the same thing. Copied constants would drift
# apart silently and turn that dispatch-time refusal back into the late failure
# it was added to remove; they live here instead.
#
# Not a general library: promotion is the only caller, and both halves run from
# a `hack`-only sparse checkout of a trusted ref, so this file has to stay
# inside hack/ next to them.

# The one OCI repository a packages candidate may ever be published to or read
# back from. Overridable only so the behavioural suites can point both halves
# at a fixture registry; production never sets it.
EXPECTED_PACKAGES_REPOSITORY="${EXPECTED_PACKAGES_REPOSITORY:-oci://ghcr.io/cozystack/cozystack/cozystack-packages}"

# An immutable Flux OCI source pin, `digest=sha256:<64 lowercase hex>`. The
# tag form Flux also accepts is exactly what promotion must neither publish nor
# accept: a stable installer pinned by tag resolves to whatever that tag points
# at later, which is how a stable install ends up serving rc content (#3477).
PACKAGES_DIGEST_REF_PATTERN='^digest=sha256:[0-9a-f]{64}$'
