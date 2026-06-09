export NAME=metallb
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add metallb https://metallb.github.io/metallb
	helm repo update metallb
	helm pull metallb/metallb --version 0.16.1 --untar --untardir charts

test:
	helm unittest .

image-controller image-speaker:
	$(eval TARGET := $(subst image-,,$@))
	$(eval VERSION := $(shell yq '.appVersion' charts/metallb/Chart.yaml))
	docker buildx build images/metallb \
		--platform=linux/amd64 \
		--target $(TARGET) \
		--build-arg VERSION=$(VERSION) \
		$(call image-tags,metallb-$(TARGET),$(VERSION)) \
		--cache-from type=registry,ref=$(CACHE_REGISTRY)/metallb-$(TARGET):latest \
		--cache-to type=inline \
		--metadata-file images/$(TARGET).json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/metallb-$(TARGET)" \
		yq -i '.metallb.$(TARGET).image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/$(TARGET).json -o json -r) \
		yq -i '.metallb.$(TARGET).image.tag = strenv(TAG)' values.yaml
	rm -f images/$(TARGET).json

image: image-controller image-speaker
