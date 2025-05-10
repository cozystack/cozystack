NAME=cozystack-api
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

image: image-cozystack-api

image-cozystack-api:
	docker buildx build -f images/cozystack-api/Dockerfile ../../.. \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/cozystack-api:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-api:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-api.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/cozystack-api:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/cozystack-api.json -o json -r)" \
		yq -i '.cozystackAPI.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-api.json
