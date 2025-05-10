S3MANAGER_TAG=v0.5.0

export NAME=s3manager-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	@echo Nothing to update

image: image-s3manager

image-s3manager:
	docker buildx build images/s3manager \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/s3manager:$(call settag,$(S3MANAGER_TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/s3manager:latest \
		--cache-to type=inline \
		--metadata-file images/s3manager.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/s3manager:$(call settag,$(S3MANAGER_TAG))@$$(yq e '."containerimage.digest"' images/s3manager.json -o json -r)" \
		> images/s3manager.tag
	rm -f images/s3manager.json
