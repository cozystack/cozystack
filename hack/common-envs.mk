# macOS-compatible sed in-place
ifeq ($(shell uname),Darwin)
    SED_INPLACE := sed -i ''
else
    SED_INPLACE := sed -i
endif

REGISTRY ?= ghcr.io/cozystack/cozystack

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

ifeq ($(COZYSTACK_VERSION),)
    $(shell git remote add upstream https://github.com/cozystack/cozystack.git || true)
    $(shell git fetch upstream --tags)
    COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags --match 'v*'))
endif
