CILIUM_TAG=$(shell awk '$$1 == "version:" {print $$2}' charts/cilium/Chart.yaml)

export NAME=cilium
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	helm repo add cilium https://helm.cilium.io/
	helm repo update cilium
	helm pull cilium/cilium --untar --untardir charts --version 1.17
	sed -i -e '/Used in iptables/d' -e '/SYS_MODULE/d' charts/cilium/values.yaml
	version=$$(awk '$$1 == "version:" {print $$2}' charts/cilium/Chart.yaml) && \
	sed -i "s/ARG VERSION=.*/ARG VERSION=v$${version}/" images/cilium/Dockerfile

image:
	docker buildx build images/cilium \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/cilium:$(call settag,$(CILIUM_TAG)) \
		--tag $(REGISTRY)/cilium:$(call settag,$(CILIUM_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cilium:latest \
		--cache-to type=inline \
		--metadata-file images/cilium.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	REPOSITORY="$(REGISTRY)/cilium" \
		yq -i '.cilium.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(call settag,$(CILIUM_TAG)) \
		yq -i '.cilium.image.tag = strenv(TAG)' values.yaml
	DIGEST=$$(yq e '."containerimage.digest"' images/cilium.json -o json -r) \
		yq -i '.cilium.image.digest = strenv(DIGEST)' values.yaml
	rm -f images/cilium.json
