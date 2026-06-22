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
BUILDER ?=
PLATFORM ?=
BUILDX_EXTRA_ARGS ?=
COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags --match 'v*'))

BUILDX_ARGS := --provenance=false --push=$(PUSH) --load=$(LOAD) \
  --label org.opencontainers.image.source=https://github.com/cozystack/cozystack \
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
define image-tags
--tag $(REGISTRY)/$(1):$(IMAGE_TAG)$(if $(filter 1,$(PUBLISH_VERSIONED)),$(if $(filter-out $(IMAGE_TAG),$(strip $(2))), --tag $(REGISTRY)/$(1):$(strip $(2))))$(if $(filter 1,$(PUBLISH_FLOATING)), --tag $(REGISTRY)/$(1):latest)
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
# $(comma) escapes the literal commas in the --cache-to value so make does not
# mis-parse them as $(if ...) argument separators.
comma := ,
cache-args = --cache-from type=registry,ref=$(CACHE_REGISTRY)/$(1):$(if $(2),$(2),buildcache)$(if $(filter 1,$(WRITE_CACHE)), --cache-to type=registry$(comma)ref=$(CACHE_REGISTRY)/$(1):$(if $(2),$(2),buildcache)$(comma)mode=max$(comma)oci-mediatypes=true$(comma)image-manifest=true)

ifeq ($(COZYSTACK_VERSION),)
    $(shell git remote add upstream https://github.com/cozystack/cozystack.git || true)
    $(shell git fetch upstream --tags)
    COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags --match 'v*'))
endif
