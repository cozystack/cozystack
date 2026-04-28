export NAME=dashboard
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image: image-token-proxy

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
