KUBERNETES_VERSION = v1.32
KUBERNETES_PKG_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

generate:
	readme-generator -v values.yaml -s values.schema.json -r README.md
	yq -o json -i '.properties.controlPlane.properties.apiServer.properties.resourcesPreset.enum = ["none","nano","micro","small","medium","large","xlarge","2xlarge"]' values.schema.json
	yq -o json -i '.properties.controlPlane.properties.controllerManager.properties.resourcesPreset.enum = ["none","nano","micro","small","medium","large","xlarge","2xlarge"]' values.schema.json
	yq -o json -i '.properties.controlPlane.properties.scheduler.properties.resourcesPreset.enum = ["none","nano","micro","small","medium","large","xlarge","2xlarge"]' values.schema.json
	yq -o json -i '.properties.controlPlane.properties.konnectivity.properties.server.properties.resourcesPreset.enum = ["none","nano","micro","small","medium","large","xlarge","2xlarge"]' values.schema.json

image: image-ubuntu-container-disk image-kubevirt-cloud-provider image-kubevirt-csi-driver image-cluster-autoscaler

image-ubuntu-container-disk:
	docker buildx build images/ubuntu-container-disk \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--build-arg KUBERNETES_VERSION=${KUBERNETES_VERSION} \
		--tag $(REGISTRY)/ubuntu-container-disk:$(call settag,$(KUBERNETES_VERSION)) \
		--tag $(REGISTRY)/ubuntu-container-disk:$(call settag,$(KUBERNETES_VERSION)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/ubuntu-container-disk:latest \
		--cache-to type=inline \
		--metadata-file images/ubuntu-container-disk.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/ubuntu-container-disk:$(call settag,$(KUBERNETES_VERSION))@$$(yq e '."containerimage.digest"' images/ubuntu-container-disk.json -o json -r)" \
		> images/ubuntu-container-disk.tag
	rm -f images/ubuntu-container-disk.json

image-kubevirt-cloud-provider:
	docker buildx build images/kubevirt-cloud-provider \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/kubevirt-cloud-provider:$(call settag,$(KUBERNETES_PKG_TAG)) \
		--tag $(REGISTRY)/kubevirt-cloud-provider:$(call settag,$(KUBERNETES_PKG_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kubevirt-cloud-provider:latest \
		--cache-to type=inline \
		--metadata-file images/kubevirt-cloud-provider.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/kubevirt-cloud-provider:$(call settag,$(KUBERNETES_PKG_TAG))@$$(yq e '."containerimage.digest"' images/kubevirt-cloud-provider.json -o json -r)" \
		> images/kubevirt-cloud-provider.tag
	rm -f images/kubevirt-cloud-provider.json

image-kubevirt-csi-driver:
	docker buildx build images/kubevirt-csi-driver \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/kubevirt-csi-driver:$(call settag,$(KUBERNETES_PKG_TAG)) \
		--tag $(REGISTRY)/kubevirt-csi-driver:$(call settag,$(KUBERNETES_PKG_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kubevirt-csi-driver:latest \
		--cache-to type=inline \
		--metadata-file images/kubevirt-csi-driver.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/kubevirt-csi-driver:$(call settag,$(KUBERNETES_PKG_TAG))@$$(yq e '."containerimage.digest"' images/kubevirt-csi-driver.json -o json -r)" \
		> images/kubevirt-csi-driver.tag
	rm -f images/kubevirt-csi-driver.json


image-cluster-autoscaler:
	docker buildx build images/cluster-autoscaler \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/cluster-autoscaler:$(call settag,$(KUBERNETES_PKG_TAG)) \
		--tag $(REGISTRY)/cluster-autoscaler:$(call settag,$(KUBERNETES_PKG_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cluster-autoscaler:latest \
		--cache-to type=inline \
		--metadata-file images/cluster-autoscaler.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/cluster-autoscaler:$(call settag,$(KUBERNETES_PKG_TAG))@$$(yq e '."containerimage.digest"' images/cluster-autoscaler.json -o json -r)" \
		> images/cluster-autoscaler.tag
	rm -f images/cluster-autoscaler.json
