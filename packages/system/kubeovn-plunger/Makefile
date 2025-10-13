export NAME=kubeovn-plunger
export NAMESPACE=cozy-kubeovn

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

image:
	docker buildx build -f images/kubeovn-plunger/Dockerfile ../../../ \
		--provenance false \
		--tag $(REGISTRY)/kubeovn-plunger:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kubeovn-plunger:latest \
		--cache-to type=inline \
		--metadata-file images/kubeovn-plunger.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/kubeovn-plunger:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/kubeovn-plunger.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/kubeovn-plunger.json
