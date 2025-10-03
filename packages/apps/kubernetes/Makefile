KUBERNETES_VERSION = v1.33
KUBERNETES_PKG_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	yq -o=json -i '.properties.version.enum = (load("files/versions.yaml") | keys)' values.schema.json
	../../../hack/update-crd.sh

image: image-ubuntu-container-disk image-kubevirt-cloud-provider image-kubevirt-csi-driver image-cluster-autoscaler

image-ubuntu-container-disk:
	docker buildx build images/ubuntu-container-disk \
		--build-arg KUBERNETES_VERSION=${KUBERNETES_VERSION} \
		--tag $(REGISTRY)/ubuntu-container-disk:$(call settag,$(KUBERNETES_VERSION)) \
		--tag $(REGISTRY)/ubuntu-container-disk:$(call settag,$(KUBERNETES_VERSION)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/ubuntu-container-disk:latest \
		--cache-to type=inline \
		--metadata-file images/ubuntu-container-disk.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/ubuntu-container-disk:$(call settag,$(KUBERNETES_VERSION))@$$(yq e '."containerimage.digest"' images/ubuntu-container-disk.json -o json -r)" \
		> images/ubuntu-container-disk.tag
	rm -f images/ubuntu-container-disk.json

image-kubevirt-cloud-provider:
	docker buildx build images/kubevirt-cloud-provider \
		--tag $(REGISTRY)/kubevirt-cloud-provider:$(call settag,$(KUBERNETES_PKG_TAG)) \
		--tag $(REGISTRY)/kubevirt-cloud-provider:$(call settag,$(KUBERNETES_PKG_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kubevirt-cloud-provider:latest \
		--cache-to type=inline \
		--metadata-file images/kubevirt-cloud-provider.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/kubevirt-cloud-provider:$(call settag,$(KUBERNETES_PKG_TAG))@$$(yq e '."containerimage.digest"' images/kubevirt-cloud-provider.json -o json -r)" \
		> images/kubevirt-cloud-provider.tag
	rm -f images/kubevirt-cloud-provider.json

image-kubevirt-csi-driver:
	docker buildx build images/kubevirt-csi-driver \
		--tag $(REGISTRY)/kubevirt-csi-driver:$(call settag,$(KUBERNETES_PKG_TAG)) \
		--tag $(REGISTRY)/kubevirt-csi-driver:$(call settag,$(KUBERNETES_PKG_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kubevirt-csi-driver:latest \
		--cache-to type=inline \
		--metadata-file images/kubevirt-csi-driver.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/kubevirt-csi-driver:$(call settag,$(KUBERNETES_PKG_TAG))@$$(yq e '."containerimage.digest"' images/kubevirt-csi-driver.json -o json -r)" \
		> images/kubevirt-csi-driver.tag
	IMAGE=$$(cat images/kubevirt-csi-driver.tag) \
		yq -i '.csiDriver.image = strenv(IMAGE)' ../../system/kubevirt-csi-node/values.yaml
	rm -f images/kubevirt-csi-driver.json


image-cluster-autoscaler:
	docker buildx build images/cluster-autoscaler \
		--tag $(REGISTRY)/cluster-autoscaler:$(call settag,$(KUBERNETES_PKG_TAG)) \
		--tag $(REGISTRY)/cluster-autoscaler:$(call settag,$(KUBERNETES_PKG_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cluster-autoscaler:latest \
		--cache-to type=inline \
		--metadata-file images/cluster-autoscaler.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/cluster-autoscaler:$(call settag,$(KUBERNETES_PKG_TAG))@$$(yq e '."containerimage.digest"' images/cluster-autoscaler.json -o json -r)" \
		> images/cluster-autoscaler.tag
	rm -f images/cluster-autoscaler.json
