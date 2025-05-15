export NAME=metallb
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	helm repo add metallb https://metallb.github.io/metallb
	helm repo update metallb
	helm pull metallb/metallb --untar --untardir charts
	rm -rf charts/metallb/charts/frr-k8s

image-controller image-speaker:
	$(eval TARGET := $(subst image-,,$@))
	$(eval VERSION := $(shell yq '.appVersion' charts/metallb/Chart.yaml))
	docker buildx build images/metallb \
		--provenance false \
		--target $(TARGET) \
		--build-arg VERSION=$(VERSION) \
		--tag $(REGISTRY)/metallb-$(TARGET):$(VERSION) \
		--cache-from type=registry,ref=$(REGISTRY)/metallb-$(TARGET):latest \
		--cache-to type=inline \
		--metadata-file images/$(TARGET).json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack"
		--load=1
	REPOSITORY="$(REGISTRY)/metallb-$(TARGET)" \
		yq -i '.metallb.$(TARGET).image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(VERSION)@$$(yq e '."containerimage.digest"' images/$(TARGET).json -o json -r) \
		yq -i '.metallb.$(TARGET).image.tag = strenv(TAG)' values.yaml
	rm -f images/$(TARGET).json

image: image-controller image-speaker
