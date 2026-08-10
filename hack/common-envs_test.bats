#!/usr/bin/env bats
# Unit tests for the OCI_EXPORT_DIR export mode wired through hack/common-envs.mk
# (#3257). A fork PR's unprivileged `pull_request` build carries no registry
# credentials, so it must export every image to a per-image OCI archive instead
# of pushing; a privileged workflow_run (e2e-fork.yaml) later pushes those
# archives to OCIR by digest. A build unit that pushes anyway dies on a fork with
# an anonymous-push `denied`, so these tests dry-run (`make -n`) a representative
# set of the root `build:` units with OCI_EXPORT_DIR set and assert that every
# image is exported as an archive and no (ungated) `docker://` push survives.
#
# Coverage includes the two units the fork split originally broke — talos (a
# skopeo copy that bypasses the image-tags macro) and capi-providers-cpprovider
# (a two-`--tag` buildx call that put two manifests in one OCI archive) — plus a
# plain image-tags package (cozystack-controller) as the happy path.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests): the
# relative `make -C packages/...` calls resolve against that cwd. This is NOT
# real bats — no run/$status/$output/setup(); use plain $(...) capture + grep,
# and the build-matrix_test.bats `if grep -q …; then echo FAIL; false; fi`
# negation idiom (cozytest runs each @test under `set -e`, which suppresses a
# bare `!`-negated pipeline, so a regression would silently pass a `! grep`).

@test "cozystack-controller exports one OCI archive and never pushes under OCI_EXPORT_DIR" {
  out=$(make -n -C packages/system/cozystack-controller image OCI_EXPORT_DIR=/tmp/ocitest IMAGE_TAG=pr-1-abc BUILDER=b)
  # Exactly one per-image OCI archive export for the single built image.
  # `grep -o | wc -l` counts OCCURRENCES: the macro expands onto a single line,
  # so `grep -c` would count that one line and report 1 however many flags survive.
  n=$(echo "$out" | grep -o -- '--output type=oci,dest=' | wc -l)
  [ "$n" -eq 1 ]
  echo "$out" | grep -q -- '--output type=oci,dest=/tmp/ocitest/cozystack-controller.oci.tar'
  # A plain image-tags package routes every tag through the macro, so under
  # export there is no docker:// ref at all.
  if echo "$out" | grep -q 'docker://'; then echo "FAIL: cozystack-controller must not push under OCI_EXPORT_DIR"; false; fi
}

@test "capi-providers-cpprovider exports one OCI archive with a single tag under OCI_EXPORT_DIR" {
  out=$(make -n -C packages/system/capi-providers-cpprovider image OCI_EXPORT_DIR=/tmp/ocitest IMAGE_TAG=pr-1-abc BUILDER=b)
  # Exactly one per-image OCI archive export.
  n=$(echo "$out" | grep -o -- '--output type=oci,dest=' | wc -l)
  [ "$n" -eq 1 ]
  echo "$out" | grep -q -- '--output type=oci,dest=/tmp/ocitest/cluster-api-control-plane-provider-kamaji.oci.tar'
  # The two-tag regression: under `--output type=oci` both --tag refs land in
  # index.json → two manifests → `skopeo copy oci-archive:…` fails with "more
  # than one image in oci". Exactly one --tag must survive under export.
  tags=$(echo "$out" | grep -o -- '--tag' | wc -l)
  [ "$tags" -eq 1 ]
  if echo "$out" | grep -q 'docker://'; then echo "FAIL: capi-providers-cpprovider must not push under OCI_EXPORT_DIR"; false; fi
}

@test "talos exports matchbox + talos archives and never pushes an ungated image under OCI_EXPORT_DIR" {
  out=$(make -n -C packages/core/talos image OCI_EXPORT_DIR=/tmp/ocitest IMAGE_TAG=pr-1-abc BUILDER=b)
  # matchbox is a buildx call through image-tags → one --output type=oci archive.
  echo "$out" | grep -q -- '--output type=oci,dest=/tmp/ocitest/matchbox.oci.tar'
  # talos is a skopeo copy that bypasses image-tags; under export it must target
  # an oci-archive whose basename is `talos` (the OCIR repo e2e-fork.yaml pushes
  # to by basename), NOT a docker:// registry (an anonymous push on a fork dies
  # with `denied` — #3257).
  echo "$out" | grep -q 'oci-archive:/tmp/ocitest/talos.oci.tar'
  # No push survives export except the PUBLISH_*=1-gated release copies, which
  # are inert here: a fork build never sets PUBLISH_*, so the shell gate expands
  # to `[ "0" = "1" ]`. Anything else pushing to docker:// is the regression.
  pushes=$(echo "$out" | grep 'docker://' | grep -v '"0" = "1"' || true)
  if [ -n "$pushes" ]; then echo "FAIL: talos pushes an ungated image under OCI_EXPORT_DIR: $pushes"; false; fi
}

@test "OCI_EXPORT_DIR suppresses release tags/pushes even with PUBLISH_*=1 (#3262 hardening)" {
  # The B2-class trap is latent: PUBLISH_* are 0 on fork PRs (the only export
  # case today), so force them on to prove the OCI_EXPORT_DIR gate holds anyway.
  # image-tags package: only the pr IMAGE_TAG survives — a versioned + :latest
  # multi-tag OCI archive holds >1 manifest and `skopeo copy oci-archive:` refuses it.
  out=$(make -n -C packages/system/cozystack-controller image OCI_EXPORT_DIR=/tmp/ocitest PUBLISH_VERSIONED=1 PUBLISH_FLOATING=1 IMAGE_TAG=pr-1-abc COZYSTACK_VERSION=0 BUILDER=b)
  tags=$(echo "$out" | grep -o -- '--tag' | wc -l)
  [ "$tags" -eq 1 ]
  if echo "$out" | grep -q -- ':latest'; then echo "FAIL: image-tags emits :latest under OCI_EXPORT_DIR"; false; fi
  # talos: its versioned/floating skopeo copies are additionally gated on an
  # empty OCI_EXPORT_DIR (`[ -z … ]`), so no ungated docker:// push survives export.
  tout=$(make -n -C packages/core/talos image OCI_EXPORT_DIR=/tmp/ocitest PUBLISH_VERSIONED=1 PUBLISH_FLOATING=1 IMAGE_TAG=pr-1-abc COZYSTACK_VERSION=0 BUILDER=b)
  pushes=$(echo "$tout" | grep 'docker://' | grep -vF '[ -z ' || true)
  if [ -n "$pushes" ]; then echo "FAIL: talos pushes an ungated image under OCI_EXPORT_DIR with PUBLISH_*=1: $pushes"; false; fi
}

@test "default path (OCI_EXPORT_DIR unset) still pushes and keeps release tags (#3262)" {
  # Every other test here sets OCI_EXPORT_DIR, so nothing covered the OFF path —
  # the one every same-repo PR, main and release build still takes. Without this
  # case a refactor of the export wiring could stop pushing altogether, or strip
  # the release tags, and the suite would stay green.
  out=$(make -n -C packages/system/cozystack-controller image PUBLISH_VERSIONED=1 PUBLISH_FLOATING=1 IMAGE_TAG=pr-1-abc COZYSTACK_VERSION=0 BUILDER=b)
  # buildx pushes, and no per-image archive is written.
  echo "$out" | grep -q -- '--push=1'
  if echo "$out" | grep -q -- '--output type=oci'; then echo "FAIL: OCI archive export leaked into the default path"; false; fi
  # image-tags expands all three release tags when not exporting: the
  # build-unique IMAGE_TAG, the versioned tag and :latest.
  tags=$(echo "$out" | grep -o -- '--tag' | wc -l)
  [ "$tags" -eq 3 ]
  echo "$out" | grep -q -- ':latest'
  # talos bypasses image-tags. Its skopeo copies must target the registry, and
  # the `[ -z "$(OCI_EXPORT_DIR)" ]` release gates must be INERT off-export
  # (expanding to `[ -z "" ]`) so a release run still pushes versioned+floating.
  tout=$(make -n -C packages/core/talos image PUBLISH_VERSIONED=1 PUBLISH_FLOATING=1 IMAGE_TAG=pr-1-abc COZYSTACK_VERSION=0 BUILDER=b)
  echo "$tout" | grep -q 'docker://.*/talos:pr-1-abc'
  echo "$tout" | grep -q 'docker://.*/talos:latest'
  if echo "$tout" | grep -q 'oci-archive:'; then echo "FAIL: talos exports an archive without OCI_EXPORT_DIR"; false; fi
}
