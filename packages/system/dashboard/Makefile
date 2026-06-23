export NAME=dashboard
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: image image-console image-token-proxy test test-go test-helm
.PHONY: build-console update-values-console
.NOTPARALLEL: image

test: test-helm test-go

test-helm:
	helm unittest .

test-go:
	cd images/token-proxy && go test ./...

image: image-console image-token-proxy

image-console: build-console update-values-console

# Console source lives in-tree under images/console (vendored from the former
# cozystack-ui repo). Build from that context directly — no clone step.
build-console:
	docker buildx build images/console \
		--file images/console/Containerfile \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=linux/amd64 \
		$(call image-tags,cozystack-ui,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(CACHE_REGISTRY)/cozystack-ui:latest \
		--cache-to type=inline \
		--metadata-file images/console.json \
		--build-arg APP_VERSION=$(COZYSTACK_VERSION) \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)

update-values-console:
	IMAGE="$(REGISTRY)/cozystack-ui:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/console.json -r)" \
		yq -i '.console.image = strenv(IMAGE)' values.yaml
	rm -f images/console.json

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
