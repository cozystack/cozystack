export NAME=dashboard
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update: update-crd update-dockerfiles
image: image-token-proxy update-tenant-text


update-dockerfiles:
	@echo Update dockerfiles manually

update-crd:
	rm -rf crds
	mkdir -p crds
	wget -O- https://github.com/PRO-Robotech/helmfile-manifests/archive/refs/heads/main.tar.gz | tar -C crds -xzvf- helmfile-manifests-main/charts/incloud-main/incloud-web-1.0.0/incloud-web/templates --strip-components=6
	rm -f crds/_helpers.tpl
	sed -i '/{{/d' crds/*.yml crds/*.yaml

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

update-tenant-text:
	sed -i 's|\($$tenantText := "\)[^"]\+|\1$(TAG)|' templates/configmap.yaml
