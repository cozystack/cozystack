REGISTRY := ghcr.io/cozystack/cozystack
PUSH := 1
LOAD := 0
COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags))
TAG = $(shell git describe --tags --exact-match 2>/dev/null || echo latest)

# Returns 'latest' if the git tag is not assigned, otherwise returns the provided value
define settag
$(if $(filter $(TAG),latest),latest,$(1))
endef

ifeq ($(COZYSTACK_VERSION),)
    $(shell git remote add upstream https://github.com/cozystack/cozystack.git || true)
    $(shell git fetch upstream --tags)
    COZYSTACK_VERSION = $(patsubst v%,%,$(shell git describe --tags))
endif

# Get the name of the selected docker buildx builder
BUILDER ?= $(shell docker buildx inspect --bootstrap | head -n2 | awk '/^Name:/{print $$NF}')
# Get platforms supported by the builder
PLATFORM ?= $(shell docker buildx ls --format=json | jq -r 'select(.Name == "$(BUILDER)") | [.Nodes[].Platforms // []] | flatten | unique | map(select(test("^linux/amd64$$|^linux/arm64$$"))) | join(",")')

