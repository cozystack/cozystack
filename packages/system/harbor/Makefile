HARBOR_VERSION = $(shell awk -F= '$$1 == "ARG VERSION" {print $$2; exit}' images/harbor/Dockerfile)
HARBOR_COMMIT = $(shell awk -F= '$$1 == "ARG COMMIT" {print $$2; exit}' images/harbor/Dockerfile)
# The chart release that ships HARBOR_VERSION; bump both together.
HARBOR_CHART_VERSION = 1.19.1

export NAME=harbor-system
export NAMESPACE=cozy-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

HARBOR_IMAGES = harbor-core harbor-jobservice harbor-portal harbor-registryctl harbor-exporter registry-photon nginx-photon trivy-adapter-photon
harbor-core_KEY = .harbor.core.image
harbor-jobservice_KEY = .harbor.jobservice.image
harbor-portal_KEY = .harbor.portal.image
harbor-registryctl_KEY = .harbor.registry.controller.image
harbor-exporter_KEY = .harbor.exporter.image
registry-photon_KEY = .harbor.registry.registry.image
nginx-photon_KEY = .harbor.nginx.image
trivy-adapter-photon_KEY = .harbor.trivy.image

.PHONY: test update image

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add harbor https://helm.goharbor.io
	helm repo update harbor
	helm pull harbor/harbor --version $(HARBOR_CHART_VERSION) --untar --untardir charts
	# values.yaml pins images built from HARBOR_VERSION, so a chart for another
	# Harbor release would silently run on mismatched images.
	test "v$$(yq e '.appVersion' charts/harbor/Chart.yaml)" = "$(HARBOR_VERSION)"

# Sequential on purpose: every image stamps the same values.yaml with yq -i.
image:
	$(foreach name,$(HARBOR_IMAGES),$(call build-harbor-image,$(name)))

define build-harbor-image
	docker buildx build $(if $(filter harbor-portal,$(1)),images/harbor-portal,images/harbor --target $(1)) \
		--build-arg VERSION=$(HARBOR_VERSION) --build-arg COMMIT=$(HARBOR_COMMIT) \
		$(call image-tags,$(1),$(HARBOR_VERSION)) \
		$(call cache-args,$(1)) \
		--metadata-file images/$(1).json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/$(1)" TAG="$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/$(1).json -o json -r)" \
		yq -i '$($(1)_KEY).repository = strenv(REPOSITORY) | $($(1)_KEY).tag = strenv(TAG)' values.yaml
	rm -f images/$(1).json

endef
