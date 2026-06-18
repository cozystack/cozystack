export NAME=dashboard
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: image image-console image-token-proxy test test-go test-helm
.PHONY: clone-console build-console update-values-console clean-console
.NOTPARALLEL: image

test: test-helm test-go

test-helm:
	helm unittest .

test-go:
	cd images/token-proxy && go test ./...

image: image-console image-token-proxy

CONSOLE_REPO ?= https://github.com/cozystack/cozystack-ui.git

# CONSOLE_BRANCH: which cozystack-ui branch to build the dashboard from.
# Release builds (tags.yaml) pass this explicitly, derived from the release
# tag's base branch (main -> main, release-X.Y -> release-X.Y), because the
# tag checkout is a detached HEAD where the branch name is unavailable. For
# local/manual builds we derive it from the current branch: a release-X.Y
# checkout builds the matching UI branch; everything else builds from main.
CONSOLE_GIT_BRANCH = $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
CONSOLE_BRANCH ?= $(if $(filter release-%,$(CONSOLE_GIT_BRANCH)),$(CONSOLE_GIT_BRANCH),main)

image-console: clone-console build-console update-values-console clean-console

clone-console:
	rm -rf .tmp-cozystack-ui
	git clone --depth 1 --branch $(CONSOLE_BRANCH) $(CONSOLE_REPO) .tmp-cozystack-ui

build-console:
	docker buildx build .tmp-cozystack-ui \
		--file .tmp-cozystack-ui/Containerfile \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=linux/amd64 \
		$(call image-tags,cozystack-ui,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(CACHE_REGISTRY)/cozystack-ui:latest \
		--cache-to type=inline \
		--metadata-file images/console.json \
		--build-arg APP_VERSION=$(COZYSTACK_VERSION) \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack-ui" \
		--load=$(LOAD)

update-values-console:
	IMAGE="$(REGISTRY)/cozystack-ui:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/console.json -r)" \
		yq -i '.console.image = strenv(IMAGE)' values.yaml

clean-console:
	rm -rf .tmp-cozystack-ui images/console.json

image-token-proxy:
	docker buildx build images/token-proxy \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=linux/amd64 \
		$(call image-tags,token-proxy,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(CACHE_REGISTRY)/token-proxy:latest \
		--cache-to type=inline \
		--metadata-file images/token-proxy.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/token-proxy:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/token-proxy.json -r)" \
		yq -i '.tokenProxy.image = strenv(IMAGE)' values.yaml
	rm -f images/token-proxy.json
