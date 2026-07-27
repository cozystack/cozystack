KUBERNETES_VERSIONS = $(shell awk -F'"' '{print $$2}' files/versions.yaml)
KUBERNETES_PKG_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test generate update

test:
	helm unittest .
	bash tests/containerd-config-path-test.sh

generate:
	cozyvalues-gen -m 'kubernetes' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/kubernetes/types.go
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate

image: image-ubuntu-container-disk image-kubevirt-cloud-provider image-kubevirt-csi-driver image-cluster-autoscaler

# One container disk per Kubernetes minor. Each build spends ~4-5min inside a
# libguestfs appliance installing that minor's kubelet/kubeadm into its own copy
# of the cloud image, and the versions share no per-version work, so build them
# concurrently: the serial loop this replaced spent ~28min of the Build job's
# 30min budget on this target alone. The sub-make carries its own -j so the root
# `make build` stays serial, and buildkit dedups the shared guestfish and
# cloud-image stages across the concurrent solves. --output-sync=target groups
# each version's log under its target name -- six interleaved buildx progress
# streams are unreadable otherwise.
image-ubuntu-container-disk:
	$(MAKE) -j$(words $(KUBERNETES_VERSIONS)) --output-sync=target \
		$(addprefix image-ubuntu-container-disk-,$(KUBERNETES_VERSIONS))

image-ubuntu-container-disk-%:
	docker buildx build images/ubuntu-container-disk \
		--build-arg KUBERNETES_VERSION=$* \
		--tag $(REGISTRY)/ubuntu-container-disk:$*-$(IMAGE_TAG) \
		$(if $(filter 1,$(PUBLISH_VERSIONED)),--tag $(REGISTRY)/ubuntu-container-disk:$*) \
		$(call cache-args,ubuntu-container-disk,$*-buildcache) \
		--metadata-file images/ubuntu-container-disk-$*.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/ubuntu-container-disk:$*-$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/ubuntu-container-disk-$*.json -o json -r)" \
		> images/ubuntu-container-disk-$*.tag
	rm -f images/ubuntu-container-disk-$*.json

image-kubevirt-cloud-provider:
	docker buildx build images/kubevirt-cloud-provider \
		$(call image-tags,kubevirt-cloud-provider,$(KUBERNETES_PKG_TAG)) \
		$(call cache-args,kubevirt-cloud-provider) \
		--metadata-file images/kubevirt-cloud-provider.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/kubevirt-cloud-provider:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubevirt-cloud-provider.json -o json -r)" \
		> images/kubevirt-cloud-provider.tag
	rm -f images/kubevirt-cloud-provider.json

image-kubevirt-csi-driver:
	docker buildx build images/kubevirt-csi-driver \
		$(call image-tags,kubevirt-csi-driver,$(KUBERNETES_PKG_TAG)) \
		$(call cache-args,kubevirt-csi-driver) \
		--metadata-file images/kubevirt-csi-driver.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/kubevirt-csi-driver:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubevirt-csi-driver.json -o json -r)" \
		> images/kubevirt-csi-driver.tag
	IMAGE=$$(cat images/kubevirt-csi-driver.tag) \
		yq -i '.csiDriver.image = strenv(IMAGE)' ../../system/kubevirt-csi-node/values.yaml
	rm -f images/kubevirt-csi-driver.json


image-cluster-autoscaler:
	docker buildx build images/cluster-autoscaler \
		$(call image-tags,cluster-autoscaler,$(KUBERNETES_PKG_TAG)) \
		$(call cache-args,cluster-autoscaler) \
		--metadata-file images/cluster-autoscaler.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/cluster-autoscaler:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/cluster-autoscaler.json -o json -r)" \
		> images/cluster-autoscaler.tag
	rm -f images/cluster-autoscaler.json

