export NAME=capi-providers-cpprovider
export NAMESPACE=cozy-cluster-api

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: image update

# COMMIT is the upstream master SHA pinned in the Dockerfile and reused
# below for the kustomize re-render. Keep them in lockstep when bumping.
COMMIT = $(shell awk -F= '/^ARG COMMIT=/{print $$2}' images/cluster-api-control-plane-provider-kamaji/Dockerfile)
IMAGE_BASE = $(REGISTRY)/cluster-api-control-plane-provider-kamaji
IMAGE_TAG = master-$(shell echo $(COMMIT) | cut -c1-7)

image:
	docker buildx build images/cluster-api-control-plane-provider-kamaji \
		--tag $(IMAGE_BASE):$(IMAGE_TAG) \
		--tag $(IMAGE_BASE):$(IMAGE_TAG)-$(TAG) \
		--cache-from type=registry,ref=$(IMAGE_BASE):latest \
		--cache-to type=inline \
		--metadata-file images/cluster-api-control-plane-provider-kamaji.json \
		$(BUILDX_ARGS)
	digest=$$(yq e '."containerimage.digest"' images/cluster-api-control-plane-provider-kamaji.json -o json -r); \
	IMG="$(IMAGE_BASE):$(IMAGE_TAG)@$$digest"; \
	echo "$$IMG" > images/cluster-api-control-plane-provider-kamaji.tag; \
	sed -i.bak -E "s|image: ghcr\.io/.+/cluster-api-control-plane-provider-kamaji:[^[:space:]]+|image: $$IMG|" files/control-plane-components.yaml; \
	rm files/control-plane-components.yaml.bak; \
	gzip -nc files/control-plane-components.yaml > files/components.gz
	rm -f images/cluster-api-control-plane-provider-kamaji.json

# Refreshes files/control-plane-components.yaml + files/components.gz from
# the upstream sources at the pinned COMMIT, leaving the manager image ref
# pointing at our mirror. Use this when bumping COMMIT in the Dockerfile.
# Requires `kustomize` on PATH.
update:
	@command -v kustomize >/dev/null || { echo "kustomize required (https://kustomize.io)"; exit 1; }
	rm -rf .update-tmp
	mkdir .update-tmp
	curl -sSL "https://github.com/clastix/cluster-api-control-plane-provider-kamaji/archive/$(COMMIT).tar.gz" \
		| tar -xzf - --strip=1 -C .update-tmp
	cd .update-tmp/config/manager && \
		kustomize edit set image controller=$(IMAGE_BASE):$(IMAGE_TAG)
	kustomize build .update-tmp/config/default > files/control-plane-components.yaml
	gzip -nc files/control-plane-components.yaml > files/components.gz
	rm -rf .update-tmp
