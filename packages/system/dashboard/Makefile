export NAME=dashboard
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: image image-console image-token-proxy
.PHONY: clone-console build-console update-values-console clean-console
.NOTPARALLEL: image

image: image-console image-token-proxy

CONSOLE_REPO ?= https://github.com/cozystack/cozystack-ui.git
CONSOLE_REF ?= dd1ff0bdbd5e6e2fb6614fd1d831c4cb5c8f8025

image-console: clone-console build-console update-values-console clean-console

clone-console:
	rm -rf .tmp-cozystack-ui
	git init .tmp-cozystack-ui
	git -C .tmp-cozystack-ui remote add origin $(CONSOLE_REPO)
	git -C .tmp-cozystack-ui fetch --depth 1 origin $(CONSOLE_REF)
	git -C .tmp-cozystack-ui checkout FETCH_HEAD

build-console:
	docker buildx build .tmp-cozystack-ui \
		--file .tmp-cozystack-ui/Containerfile \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=linux/amd64 \
		--tag $(REGISTRY)/cozystack-ui:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-ui:latest \
		--cache-to type=inline \
		--metadata-file images/console.json \
		--build-arg APP_VERSION=$(COZYSTACK_VERSION) \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack-ui" \
		--load=$(LOAD)

update-values-console:
	IMAGE="$(REGISTRY)/cozystack-ui:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/console.json -r)" \
		yq -i '.console.image = strenv(IMAGE)' values.yaml

clean-console:
	rm -rf .tmp-cozystack-ui images/console.json

image-token-proxy:
	docker buildx build images/token-proxy \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=linux/amd64 \
		--tag $(REGISTRY)/token-proxy:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/token-proxy:latest \
		--cache-to type=inline \
		--metadata-file images/token-proxy.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/token-proxy:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/token-proxy.json -r)" \
		yq -i '.tokenProxy.image = strenv(IMAGE)' values.yaml
	rm -f images/token-proxy.json
