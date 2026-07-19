#!/usr/bin/env bats
# Unit test: mongodb-rd cozyrds must deliver the TLS trust anchor without
# private key material.
#
# The PSMDB operator creates <release>-ca-cert holding the CA PRIVATE KEY and
# <release>-ssl / <release>-ssl-internal holding server private keys. The tenant
# must receive only the key-free <release>.tenant-ca projection, reached by
# label. These names are the contract between this file and the operator, and
# nothing else in the repo checks them at this level: end-to-end coverage
# cannot reach the projection until the CA-extraction controller lands, since
# the helm-controller refuses the caCert field while the CRD does not declare
# it and the mongodb-rd release never reconciles.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/mongodb-rd/cozyrds/mongodb.yaml"
CRD="$REPO_ROOT/packages/system/application-definition-crd/definition/cozystack.io_applicationdefinitions.yaml"
# The path every check below walks to reach the ApplicationDefinition's declared
# spec fields. Bound once so the positive control and the assertion it protects
# cannot drift apart.
CRD_SPEC_PROPS='.spec.versions[].schema.openAPIV3Schema.properties.spec.properties'
# Bound explicitly rather than read from BATS_TEST_FILENAME inside a test body.
# These files run under hack/cozytest.sh, not bats: it executes each body with
# set -u and never exports that variable, so a bare reference aborts the test —
# and the runner stops at the first failure, taking every later test with it.
# $0 is not a usable fallback either, since it expands to the runner's own path
# and would silently scan the wrong file.
SELF="$REPO_ROOT/hack/check-mongodb-rd-secrets.bats"

@test "mongodb-rd cozyrds declares the operator's CA secret as the trust-anchor source" {
  grep -q 'sourceSecretName: "{{ .release }}-ca-cert"' "$COZYRDS"
}

@test "mongodb-rd cozyrds reads ca.crt from that source, never a key" {
  grep -q "sourceKey: ca.crt" "$COZYRDS"
  # Written as explicit if/exit rather than `! grep ...`. POSIX exempts a
  # negated command from `set -e`, so a non-final `! grep` line can fail and
  # the body carries on regardless — the assertion would be dead, and the last
  # one alive only by accident of position.
  if grep -qE "sourceKey: (tls|ca)\.key" "$COZYRDS"; then
    echo "sourceKey names private key material" >&2
    exit 1
  fi
}

@test "mongodb-rd checks use the set -e-safe negation form" {
  # Guards the pattern itself, so the next negated assertion added here cannot
  # be silently dead. `!`-prefixed commands are exempt from set -e.
  # SELF is a hardcoded path and grep exits 2 on a missing file, which an `if`
  # reads as simply false — so renaming this file would turn the guard green
  # while it inspected nothing. Every other check here reads $COZYRDS through
  # `grep -q` or a `$(yq ...)` assignment, both of which abort under set -e;
  # this is the one input that can vanish quietly.
  if [ ! -f "$SELF" ]; then
    echo "SELF does not resolve ($SELF); this guard would be vacuous" >&2
    exit 1
  fi
  # [[:space:]] rather than \s: the latter is a GNU/PCRE extension, not POSIX
  # ERE, and degrades to a literal 's' on a BSD grep — which would make this
  # guard vacuous, the one property a vacuity guard must not have.
  if grep -nE '^[[:space:]]*![[:space:]]' "$SELF"; then
    echo "Negated assertion above is exempt from set -e; use if/exit 1 instead" >&2
    exit 1
  fi
}

@test "mongodb-rd cozyrds selects the key-free projection by label" {
  grep -q 'internal.cozystack.io/tenant-ca: "true"' "$COZYRDS"
}

@test "mongodb-rd cozyrds secrets.include path resolves" {
  # Positive control for the two structural checks below. Both walk
  # .spec.secrets.include, and yq returns 0 for a path that does not exist — so
  # a rename of that field would leave them green while asserting nothing. The
  # grep-based tests would not notice either, since the strings stay in the
  # file. Deleting `include` outright is already caught by the label grep; this
  # covers the rename.
  n="$(yq '.spec.secrets.include // [] | length' "$COZYRDS")"
  if [ "$n" -lt 2 ]; then
    echo "spec.secrets.include has $n entries; the structural checks would be vacuous" >&2
    exit 1
  fi
}

@test "mongodb-rd cozyrds has no catch-all secrets selector" {
  # The mine this guards: an include entry with an empty matchLabels and no
  # resourceNames. LabelSelectorAsSelector({}) resolves to labels.Everything()
  # and a nil resourceNames matches any name, so such an entry promotes EVERY
  # Secret in the namespace — including the key-bearing TLS Secrets — to the
  # tenant. Checked structurally rather than by grep, because the correct
  # selector being present says nothing about a second, wider one alongside it.
  empty="$(yq '[.spec.secrets.include[]
    | select((.matchLabels // {} | length) == 0 and (.resourceNames // [] | length) == 0)]
    | length' "$COZYRDS")"
  if [ "$empty" != "0" ]; then
    echo "Found $empty secrets.include entry/entries matching every Secret in the namespace" >&2
    exit 1
  fi
}

@test "mongodb-rd cozyrds does not hand the tenant any key-bearing secret by name" {
  # resourceNames entries are name grants. The CA and leaf secrets each carry a
  # private key, so neither may appear as one.
  #
  # Matched on the suffix over the parsed resourceNames lists, not on a literal
  # spelling: the same object can be written "mongodb-{{ .name }}-ca-cert" or
  # "{{ .release }}-ca-cert", and a grep pinned to either form misses the other.
  # Walking the yq path also confines the check to grants — sourceSecretName
  # names the same CA secret legitimately, as an extraction source rather than
  # a tenant grant, and never appears on this path.
  leaks="$(yq '[.spec.secrets.include[].resourceNames[]?
    | select(test("-(ca-cert|ssl|ssl-internal|ssl-old|ssl-internal-old)$"))]
    | length' "$COZYRDS")"
  if [ "$leaks" != "0" ]; then
    echo "Found $leaks key-bearing secret(s) granted to the tenant by name" >&2
    exit 1
  fi
}

@test "mongodb-rd cozyrds excludes every key-bearing secret" {
  # The exclude list is the structural backstop behind the label-only include
  # selector, and nothing else pins it: deleting it leaves every other check in
  # this file green. Asserted positively — a count alone would go vacuous on a
  # renamed path, the same way the include checks would without their own
  # positive control.
  for name in ca-cert ssl ssl-internal ssl-old ssl-internal-old \
             percona-server-mongodb-users mongodb-encryption-key mongodb-keyfile s3-creds; do
    hits="$(yq "[.spec.secrets.exclude[].resourceNames[]? | select(. == \"mongodb-{{ .name }}-$name\")] | length" "$COZYRDS")"
    if [ "$hits" != "1" ]; then
      echo "mongodb-{{ .name }}-$name is not excluded (found $hits entries)" >&2
      exit 1
    fi
  done
}

@test "mongodb-rd cozyrds excludes the operator-internal secrets" {
  # These carry the internal- prefix rather than a release suffix, so they do
  # not fit the loop above. Quoted: the template braces contain spaces, and an
  # unquoted list item would word-split into "internal-mongodb-{{", ".name",
  # "}}" and assert on three names that do not exist.
  for name in "internal-mongodb-{{ .name }}" "internal-mongodb-{{ .name }}-users"; do
    hits="$(yq "[.spec.secrets.exclude[].resourceNames[]? | select(. == \"$name\")] | length" "$COZYRDS")"
    if [ "$hits" != "1" ]; then
      echo "$name is not excluded (found $hits entries)" >&2
      exit 1
    fi
  done
}

@test "ApplicationDefinition CRD does not declare caCert yet, so the declaration is not live" {
  # An activation signal, deliberately red on the transition rather than on a
  # regression.
  #
  # While this passes, the caCert block above is declared but has no effect,
  # and the release carrying it does not install at all: the helm-controller
  # builds a typed patch from the CRD schema, refuses a field the schema does
  # not declare, and mongodb-rd fails to reconcile. The red end-to-end run is
  # that state, and it is what enforces the merge order.
  #
  # When the CA-extraction controller lands its Go type and regenerated CRD,
  # this check goes red. That is the point. Everything else in this file greps
  # the cozyrds source and would stay green through that transition, and the
  # end-to-end run only flips from red to green — neither prompts anyone to
  # confirm that the shape declared here matches the shape that shipped, or to
  # assert that the projection is actually published. This does. On the flip:
  # verify sourceSecretName and sourceKey against the landed field, check the
  # projection converges, and replace this check with a real assertion on it.
  known="$(yq "[$CRD_SPEC_PROPS | keys | .[]] | length" "$CRD")"
  if [ "$known" -lt 6 ]; then
    echo "spec.properties resolved to $known fields; the assertion below would be vacuous" >&2
    exit 1
  fi

  # A second route to the same activation, worth separating because it arrives
  # without the field ever appearing in properties: with this flag set the
  # schema stops being closed, the typed patch accepts caCert, and the release
  # installs while the field remains undeclared. That state needs looking at
  # rather than passing silently — the block would be stored but not typed.
  preserve="$(yq '[.spec.versions[].schema.openAPIV3Schema.properties.spec."x-kubernetes-preserve-unknown-fields" | select(. == true)] | length' "$CRD")"
  if [ "$preserve" != "0" ]; then
    echo "spec preserves unknown fields; caCert would be accepted without being declared" >&2
    exit 1
  fi

  hits="$(yq "[$CRD_SPEC_PROPS | keys | .[] | select(. == \"caCert\")] | length" "$CRD")"
  if [ "$hits" != "0" ]; then
    echo "CRD now declares spec.caCert: the declaration is live — confirm it matches the landed field, then assert the projection directly" >&2
    exit 1
  fi
}

@test "mongodb-rd cozyrds still delivers the credentials secret" {
  # The exclude list added alongside the trust anchor is evaluated BEFORE
  # include and wins outright, so it now sits upstream of this pre-existing
  # grant. Every other line in that block is about withholding a Secret, which
  # makes adding this one to it a plausible edit — and one that would silently
  # revoke the tenant's access to their own connection string. Assert both
  # directions.
  inc="$(yq '[.spec.secrets.include[].resourceNames[]? | select(. == "mongodb-{{ .name }}-credentials")] | length' "$COZYRDS")"
  if [ "$inc" != "1" ]; then
    echo "credentials is not granted via include (found $inc entries)" >&2
    exit 1
  fi
  exc="$(yq '[.spec.secrets.exclude[].resourceNames[]? | select(. == "mongodb-{{ .name }}-credentials")] | length' "$COZYRDS")"
  if [ "$exc" != "0" ]; then
    echo "credentials appears in exclude, which wins over include" >&2
    exit 1
  fi
}
