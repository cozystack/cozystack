export NAME=flux-plunger
export NAMESPACE=cozy-fluxcd

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build -f images/flux-plunger/Dockerfile ../../../ \
		--provenance false \
		$(call image-tags,flux-plunger,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(REGISTRY)/flux-plunger:latest \
		--cache-to type=inline \
		--metadata-file images/flux-plunger.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/flux-plunger:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/flux-plunger.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/flux-plunger.json
