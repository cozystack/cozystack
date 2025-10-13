REGISTRY ?= ghcr.io/cozystack/cozystack
TAG = $(shell git describe --tags --exact-match 2>/dev/null || echo latest)
PUSH := 1
LOAD := 0
BUILDER ?=
PLATFORM ?= 
BUILDX_EXTRA_ARGS ?=
COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags))

BUILDX_ARGS := --provenance=false --push=$(PUSH) --load=$(LOAD) \
  --label org.opencontainers.image.source=https://github.com/cozystack/cozystack \
  $(if $(strip $(BUILDER)),--builder=$(BUILDER)) \
  $(if $(strip $(PLATFORM)),--platform=$(PLATFORM)) \
  $(BUILDX_EXTRA_ARGS)

# Returns 'latest' if the git tag is not assigned, otherwise returns the provided value
define settag
$(if $(filter $(TAG),latest),latest,$(1))
endef

ifeq ($(COZYSTACK_VERSION),)
    $(shell git remote add upstream https://github.com/cozystack/cozystack.git || true)
    $(shell git fetch upstream --tags)
    COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags))
endif

