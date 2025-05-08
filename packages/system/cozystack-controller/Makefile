NAME=cozystack-controller
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

image: image-cozystack-controller update-version

image-cozystack-controller:
	docker buildx build -f images/cozystack-controller/Dockerfile ../../.. \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/cozystack-controller:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-controller:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-controller.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/cozystack-controller:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/cozystack-controller.json -o json -r)" \
		yq -i '.cozystackController.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-controller.json

update-version:
	TAG="$(call settag,$(TAG))" \
		yq -i '.cozystackController.cozystackVersion = strenv(TAG)' values.yaml
