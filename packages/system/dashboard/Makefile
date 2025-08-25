export NAME=dashboard
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update: update-crd update-dockerfiles
image: image-openapi-ui image-openapi-ui-k8s-bff


update-dockerfiles:
	@echo Update dockerfiles manually

update-crd:
	rm -rf crds
	mkdir -p crds
	wget -O- https://github.com/PRO-Robotech/helmfile-manifests/archive/refs/heads/main.tar.gz | tar -C crds -xzvf- helmfile-manifests-main/charts/incloud-main/incloud-web-1.0.0/incloud-web/templates --strip-components=6
	rm -f crds/_helpers.tpl
	sed -i '/{{/d' crds/*.yml crds/*.yaml

image-openapi-ui:
	docker buildx build images/openapi-ui \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=linux/amd64 \
		--tag $(REGISTRY)/openapi-ui:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/openapi-ui:latest \
		--cache-to type=inline \
		--metadata-file images/openapi-ui.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/openapi-ui:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/openapi-ui.json -r)" \
		yq -i '.openapiUI.image = strenv(IMAGE)' values.yaml
	rm -f images/openapi-ui.json

image-openapi-ui-k8s-bff:
	docker buildx build images/openapi-ui-k8s-bff \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=linux/amd64 \
		--tag $(REGISTRY)/openapi-ui-k8s-bff:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/openapi-ui-k8s-bff:latest \
		--cache-to type=inline \
		--metadata-file images/openapi-ui-k8s-bff.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/openapi-ui-k8s-bff:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/openapi-ui-k8s-bff.json -r)" \
		yq -i '.openapiUIK8sBff.image = strenv(IMAGE)' values.yaml
	rm -f images/openapi-ui-k8s-bff.json
