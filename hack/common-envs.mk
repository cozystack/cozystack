# macOS-compatible sed in-place
ifeq ($(shell uname),Darwin)
    SED_INPLACE := sed -i ''
else
    SED_INPLACE := sed -i
endif

REGISTRY ?= ghcr.io/cozystack/cozystack

# CACHE_REGISTRY: registry holding the `:buildcache` mode=max cache images that
# `--cache-from`/`--cache-to` read and write (see the cache-args macro below).
# Defaults to $(REGISTRY) so the cache is co-located with the build registry —
# in CI that is the per-CI registry the runners are closest to, keeping cache
# transfers fast and egress-free.
CACHE_REGISTRY ?= $(REGISTRY)

# IMAGE_TAG: build-unique tag pushed for every image. Set by CI to a value
# that does not collide between concurrent builds:
#   * pull-requests.yaml -> pr-<N>-<sha>
#   * tags.yaml          -> <ref_name>           (e.g. v1.5.0)
#   * local              -> dev                  (with PUSH=0 by default)
IMAGE_TAG ?= dev

# Opt-in extra tags. Workflows set these explicitly; defaults are off so a
# local `make build` never races with CI or accidentally moves :latest.
#   PUBLISH_VERSIONED=1 -> also push :<component-version> (release semantics)
#   PUBLISH_FLOATING=1  -> also push :latest             (release/main only)
PUBLISH_VERSIONED ?= 0
PUBLISH_FLOATING  ?= 0

# WRITE_CACHE=1 enables `--cache-to` (mode=max) to CACHE_REGISTRY/<img>:buildcache.
# Default off: only the serialized main/release cache-warming build writes the
# shared cache ref. PR builds keep it 0 and read-only, so concurrent PR builds
# never race on the cache manifest (the 409 collisions PR #2711 fixed for tags).
WRITE_CACHE ?= 0

PUSH := 1
LOAD := 0

# OCI_EXPORT_DIR: when set, build every image to a per-image OCI archive
# (<dir>/<name>.oci.tar) instead of pushing or loading it. Fork PRs use this:
# the unprivileged `pull_request` build carries no registry credentials, so it
# exports the images as workflow artifacts and a privileged `workflow_run` later
# pushes them to the registry BY DIGEST (see .github/workflows/e2e-fork.yaml).
# The image digest is content-addressed and captured via --metadata-file
# regardless of output type, so the values.yaml/.tag refs baked during this
# build match exactly what the privileged run pushes. Setting it forces
# PUSH/LOAD off so buildx emits only the archive — a `--push` alongside the OCI
# --output would attempt an anonymous push and die with `denied` (#3257).
OCI_EXPORT_DIR ?=
ifneq ($(strip $(OCI_EXPORT_DIR)),)
    PUSH := 0
    LOAD := 0
endif

BUILDER ?=
PLATFORM ?=
BUILDX_EXTRA_ARGS ?=
COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags --match 'v*'))

# SBOM=1 attaches a CycloneDX SBOM as a buildx build attestation (--sbom=true).
# Requires the docker-container buildx driver and registry referrers/attestation
# support. Kept OFF by default until an OCIR referrers/attestation probe confirms
# support — otherwise the attestation push fails the build. (NB: the dashboard
# package's bespoke buildx calls don't read BUILDX_ARGS, so they are not yet
# SBOM-covered — follow-up.) See #2937.
SBOM ?= 0

BUILDX_ARGS := --provenance=false --push=$(PUSH) --load=$(LOAD) \
  --label org.opencontainers.image.source=https://github.com/cozystack/cozystack \
  $(if $(filter 1,$(SBOM)),--sbom=true) \
  $(if $(strip $(BUILDER)),--builder=$(BUILDER)) \
  $(if $(strip $(PLATFORM)),--platform=$(PLATFORM)) \
  $(BUILDX_EXTRA_ARGS)

# image-tags <repo> <versioned-tag>
# Expands to one or more `--tag` flags for `docker buildx build`:
#   - always:                 :$(IMAGE_TAG)        (build-unique handle)
#   - if PUBLISH_VERSIONED=1: :<versioned-tag>     (skipped when arg2 is empty
#                                                  or equals IMAGE_TAG)
#   - if PUBLISH_FLOATING=1:  :latest
# Consumers reference images by digest, so the build-unique tag is enough
# for cluster-side pulls; the versioned and floating tags exist for human
# discoverability and downstream tooling on releases.
# When OCI_EXPORT_DIR is set it also appends a per-image `--output type=oci`
# (via the oci-output macro) so the build writes <dir>/<name>.oci.tar (the tag
# becomes the archive's image name). Every package's buildx call routes its tags
# through this macro, so this is the single point that turns the whole build into
# an artifact-export build — except recipes that bypass image-tags, which must
# call oci-output directly (see #3257).

# comma emits a literal comma inside the lazily-expanded macros below
# (oci-output, cache-args) without make mis-reading it as an $(if …)/$(call …)
# argument separator. Defined here, above its first user, so it is unambiguously
# in scope wherever those macros are $(call)ed from a recipe.
comma := ,

# oci-output <image-name> — when OCI_EXPORT_DIR is set, expands to a per-image
# `--output type=oci,dest=<dir>/<name>.oci.tar`; empty otherwise. EVERY buildx
# call reachable from a fork build must include this — via image-tags, or
# directly for recipes that bypass image-tags (#3257). A missing archive is
# caught loudly by the fork build's `if-no-files-found: error` upload.
oci-output = $(if $(strip $(OCI_EXPORT_DIR)), --output type=oci$(comma)dest=$(OCI_EXPORT_DIR)/$(subst /,-,$(1)).oci.tar)

# image-tags <image-name> [<versioned-tag>] — the --tag/--output flags for one
# image. Under OCI_EXPORT_DIR the versioned/floating tags are suppressed: a
# multi-`--tag` `--output type=oci` build writes every ref into the archive's
# index.json, and `skopeo copy oci-archive:<f>` then refuses it ("more than one
# image in oci"). PUBLISH_* are 0 on fork PRs (the only export case today), so
# this is belt-and-suspenders for that trap.
define image-tags
--tag $(REGISTRY)/$(1):$(IMAGE_TAG)$(if $(strip $(OCI_EXPORT_DIR)),,$(if $(filter 1,$(PUBLISH_VERSIONED)),$(if $(filter-out $(IMAGE_TAG),$(strip $(2))), --tag $(REGISTRY)/$(1):$(strip $(2))))$(if $(filter 1,$(PUBLISH_FLOATING)), --tag $(REGISTRY)/$(1):latest))$(call oci-output,$(1))
endef

# cache-args <image-name> [<cache-tag>]
# Expands to buildx cache flags for one image:
#   --cache-from is always emitted (a missing cache 404s harmlessly -> cold build)
#   --cache-to is emitted only when WRITE_CACHE=1 (main/release), writing mode=max
#     so ALL stages are cached -- including the multistage `builder` layers that
#     `--cache-to type=inline` could never export. oci-mediatypes + image-manifest
#     keep the cache manifest portable across registries (OCIR/ghcr/ECR).
# <cache-tag> defaults to `buildcache`; pass an explicit tag for images that build
# a distinct artifact per loop iteration (e.g. ubuntu-container-disk per k8s ver).
# $(comma) (defined above) escapes the literal commas in the --cache-to value so
# make does not mis-parse them as $(if ...) argument separators.
cache-args = --cache-from type=registry,ref=$(CACHE_REGISTRY)/$(1):$(if $(2),$(2),buildcache)$(if $(filter 1,$(WRITE_CACHE)), --cache-to type=registry$(comma)ref=$(CACHE_REGISTRY)/$(1):$(if $(2),$(2),buildcache)$(comma)mode=max$(comma)oci-mediatypes=true$(comma)image-manifest=true)

ifeq ($(COZYSTACK_VERSION),)
    $(shell git remote add upstream https://github.com/cozystack/cozystack.git || true)
    $(shell git fetch upstream --tags)
    COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags --match 'v*'))
endif
