export NAME=capi-providers-cpprovider
export NAMESPACE=cozy-cluster-api

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: image update

# Upstream tag pinned in the Dockerfile and reused below for the
# kustomize re-render. Keep them in lockstep when bumping.
UPSTREAM_VERSION = $(shell awk -F= '/^ARG VERSION=/{print $$2}' images/cluster-api-control-plane-provider-kamaji/Dockerfile)
# Counter for cozystack-side respins on top of the same UPSTREAM_VERSION
# (e.g. patches/ refresh, Go toolchain bump). Bump when something other
# than UPSTREAM_VERSION changes.
COZYSTACK_REV = 0
IMAGE_BASE = $(REGISTRY)/cluster-api-control-plane-provider-kamaji
# The image is versioned by its upstream component, so the stamped refs carry
# this tag. It is pushed like any package's versioned tag, only under
# PUBLISH_VERSIONED=1; every build pushes the build-unique IMAGE_TAG.
COMPONENT_VERSION = $(UPSTREAM_VERSION)-cozystack.$(COZYSTACK_REV)

image:
	docker buildx build images/cluster-api-control-plane-provider-kamaji \
		$(call image-tags,cluster-api-control-plane-provider-kamaji,$(COMPONENT_VERSION)) \
		--cache-from type=registry,ref=$(IMAGE_BASE):latest \
		$(if $(strip $(OCI_EXPORT_DIR)),,--cache-to type=inline) \
		--metadata-file images/cluster-api-control-plane-provider-kamaji.json \
		$(BUILDX_ARGS)
	digest=$$(yq e '."containerimage.digest"' images/cluster-api-control-plane-provider-kamaji.json -o json -r); \
	IMG="$(IMAGE_BASE):$(COMPONENT_VERSION)@$$digest"; \
	echo "$$IMG" > images/cluster-api-control-plane-provider-kamaji.tag; \
	sed -i.bak -E "s|image: ghcr\.io/.+/cluster-api-control-plane-provider-kamaji:[^[:space:]]+|image: $$IMG|" files/control-plane-components.yaml; \
	rm files/control-plane-components.yaml.bak; \
	gzip -nc files/control-plane-components.yaml > files/components.gz
	rm -f images/cluster-api-control-plane-provider-kamaji.json

# Refreshes files/control-plane-components.yaml + files/components.gz from
# upstream sources at UPSTREAM_VERSION with patches/ applied, leaving the
# manager image ref pointing at our mirror. Use when bumping UPSTREAM_VERSION
# or editing patches/. Requires kustomize on PATH.
update:
	@command -v kustomize >/dev/null || { echo "kustomize required (https://kustomize.io)"; exit 1; }
	rm -rf .update-tmp
	mkdir .update-tmp
	curl -sSL "https://github.com/clastix/cluster-api-control-plane-provider-kamaji/archive/refs/tags/$(UPSTREAM_VERSION).tar.gz" \
		| tar -xzf - --strip=1 -C .update-tmp
	cd .update-tmp && git init -q && git add . && git commit -qm init && \
		for p in ../images/cluster-api-control-plane-provider-kamaji/patches/*.diff; do \
			[ -e "$$p" ] || continue; \
			git apply --recount "$$p"; \
		done
	cd .update-tmp/config/manager && \
		kustomize edit set image controller=$(IMAGE_BASE):$(COMPONENT_VERSION)
	kustomize build .update-tmp/config/default > files/control-plane-components.yaml
	gzip -nc files/control-plane-components.yaml > files/components.gz
	rm -rf .update-tmp
