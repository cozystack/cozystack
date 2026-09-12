NAME=vyos-router-image
NAMESPACE=cozy-system

# ── Pinned appliance inputs (reproducibility levers) ─────────────────────────
# The appliance is no longer built from source against an apt repository. It is
# derived from a PUBLISHED VyOS Stream image, and every input below is a fixed
# artifact that upstream keeps: Stream releases are archived, so a pin here does
# not rot the way a rolling one does.
#
# That is the whole reason for the change. The rolling repository retains only
# the current kernel, and vyos-build raises `kernel_version` before the matching
# kernel is published, so a pin was unbuildable both when it aged and, briefly,
# when it was fresh. The retention clock ran on upstream's schedule, and on
# 2026-09-08 it took the pinned kernel away mid-flight: `E: Unable to locate
# package linux-image-6.18.48-vyos`, hours after that same package had built.
# The VyOS LTS and per-release package repositories are not open to the public,
# so there was no repository to move to — only a way to stop needing one.
#
# What Stream costs, stated plainly: the image is whatever upstream cut, so no
# VyOS package can be added to it. Anything the appliance needs on top must come
# from Debian, which the image is built on, or be a file. hack/inject-appliance.sh
# is where that happens and what it does is the appliance's real recipe now.
#
# VYOS_STREAM_RELEASE is a MANUAL bump and has no Renovate manager: VyOS publishes
# Stream releases as a page rather than a feed, and a manager pointed at a
# datasource that does not exist is worse than a manual pin. It is also
# the one bump that is never urgent — Stream releases are archived, so this pin
# stays buildable indefinitely, which is the whole reason for using it.
#
# Bumping is coupled, though: the image and the site-router configuration contract
# advance together, and the config.boot footer carries the release's own component
# versions, so a bump needs that footer re-captured. See
# packages/apps/site-router/docs/image-lifecycle.md. Releases are listed at
# https://vyos.net/get/stream/ and the checksum is over the ISO as downloaded —
# take it from the download, not from the page.
VYOS_STREAM_RELEASE = 2026.03
VYOS_ISO_URL    = https://community-downloads.vyos.dev/stream/$(VYOS_STREAM_RELEASE)/vyos-$(VYOS_STREAM_RELEASE)-generic-amd64.iso
VYOS_ISO_SHA256 = 56151a536e4a70c1a3f9202d8e6e59e7dd308cc84e2c50b633884a1376a39010

# vyos-build is still used, but only for scripts/image-build/raw_image.py, which
# installs a published ISO onto a disk image. `--reuse-iso` short-circuits
# live-build entirely, so VYOS_BUILD_REF no longer selects a kernel and cannot
# age out against a mirror — the container and the ref are pinned for the sake of
# the conversion behaving the same way twice, nothing more.
VYOS_BUILD_IMAGE = vyos/vyos-build:current@sha256:77b88166bb9dfb2fdef4f870064e579a1e33247a52c03a12c1703bf9b854a4c5
VYOS_BUILD_REF   = d2525d97a11de9f4799dbb6d425c5c9c51edf179

# The guest agent, and the one dependency the Stream image lacks. Both are plain
# Debian bookworm packages — the same base the appliance is built on — pinned by
# version and digest because they are fetched at build time. They are NOT on any
# VyOS repository; qemu-guest-agent never was.
#
# Unlike the Stream ISO these DO rot: a `+deb12uN` package is a security-update
# stream, and when it is superseded the old file leaves the pool. The SHA256
# check turns that into a loud build failure rather than a silent substitution,
# but a loud failure whose first sighting is a red release build is still a
# deadline nobody was told about — so both are covered by a Renovate deb-datasource
# manager (.github/renovate.json). That is also why the `+` in the version is
# written literally rather than percent-encoded: the pool serves both forms
# (verified), and only the literal one is a Debian version string a datasource
# can compare.
VYOS_DEB_URLS = \
  http://deb.debian.org/debian/pool/main/libu/liburing/liburing2_2.3-3_amd64.deb \
  http://deb.debian.org/debian/pool/main/q/qemu/qemu-guest-agent_7.2+dfsg-7+deb12u18+b3_amd64.deb
VYOS_DEB_SHA256 = \
  c23077e3640e6cb4b819c134b3f41d5cf21b3edac099654b1b4142c3069a39a3 \
  ffdc7852decec129a903acf1b9ac083859cabee60e7b24477b652f36e228e808

# Names the snapshot in the artifact and the image tag, and is otherwise inert.
# Keep it a function of VYOS_STREAM_RELEASE so it cannot drift from what shipped.
VYOS_VERSION     = stream-$(VYOS_STREAM_RELEASE)
VYOS_ARCH        = amd64

# ── Why this package stays in the root Makefile `build:` list ────────────────
# It is a heavy privileged build behind two external fetches, and the PR lane
# deliberately keeps it out of the per-package build matrix for exactly that
# reason (a dedicated, path-scoped build-vyos job). The release lanes have no
# such scoping, so it runs on every merge to a maintained line — which looks
# like an asymmetry worth closing by dropping it from `build:`.
#
# It is not. docs/agents/image-refs.md makes it an invariant: "A package that
# stamps a reference is in the root Makefile build: list… a package outside it
# is never rebuilt by any path and its pin freezes permanently." This package
# stamps packages/apps/site-router/images/vyos-router-disk.tag, so removing it
# would put the appliance in the same class as the live violations that doc
# names. The PR lane can scope it out precisely because the release lanes do not.
#
# The availability argument the PR-lane scoping rests on also no longer
# describes this build. It was written against the rolling apt repository, whose
# index moved under a running build and whose kernel retention gave a pin a
# shelf life. Every input is now a fixed artifact: an archived Stream ISO, a
# digest-pinned container, a git ref and two Debian pool files.
include ../../../hack/common-envs.mk

QCOW2 = ../../../_out/assets/vyos-router-$(VYOS_ARCH).qcow2

# Package the qcow2 as a KubeVirt containerDisk OCI image, push it, and stamp the
# digest .tag into the CONSUMING chart (packages/apps/site-router), whose boot
# DataVolume imports it via CDI's registry importer — mirroring how the talos
# build stamps bootbox/images/matchbox.tag. In CI the build-vyos job's `git diff`
# captures that stamped .tag as a patch fragment, which `finalize` reassembles
# into pr.patch so e2e resolves the freshly-built digest.
image: image-vyos-router-disk

image-vyos-router-disk: qcow2
	docker buildx build -f images/vyos-router-disk/Dockerfile ../../.. \
		$(call image-tags,vyos-router-disk,$(VYOS_VERSION)) \
		--metadata-file images/vyos-router-disk.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/vyos-router-disk:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/vyos-router-disk.json -o json -r)" \
		> ../../apps/site-router/images/vyos-router-disk.tag
	rm -f images/vyos-router-disk.json

# Heavy privileged live-build step; produces $(QCOW2). Kept idempotent so a rerun
# reuses an already-built disk.
qcow2:
	test -f $(QCOW2) || \
		VYOS_BUILD_IMAGE="$(VYOS_BUILD_IMAGE)" \
		VYOS_BUILD_REF="$(VYOS_BUILD_REF)" \
		VYOS_VERSION="$(VYOS_VERSION)" \
		VYOS_ARCH="$(VYOS_ARCH)" \
		VYOS_ISO_URL="$(VYOS_ISO_URL)" \
		VYOS_ISO_SHA256="$(VYOS_ISO_SHA256)" \
		VYOS_DEB_URLS="$(VYOS_DEB_URLS)" \
		VYOS_DEB_SHA256="$(VYOS_DEB_SHA256)" \
		hack/build-qcow2.sh

# No vendored upstream chart to pull, and the build-input pins above are
# refreshed by Renovate rather than by `make update`.
update: ;
