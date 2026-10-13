include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test generate update image kairos-worker-os-test kairos-worker-base kairos-worker-raw

test:
	helm unittest .

generate:
	cozyvalues-gen -m 'kubernetesnodes' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/kubernetesnodes/types.go
	../../../hack/update-crd.sh

# Keep the worker-image inputs (Talos<->Kubernetes support matrix and the
# kubectl.tag used by the talos-reconcile Job) in lockstep with the parent
# kubernetes chart. They are copied, not symlinked, so `helm package` bundles
# them; run this after the parent chart's `make update`.
#
# _talosconfigtemplate.tpl is not copied from anywhere: the parent chart no
# longer carries worker node pools, so this chart owns the worker machine
# config outright. It stays here because the TalosConfigTemplate it renders is
# content-hash named and both consumers in this chart - nodegroup.yaml, which
# writes bootstrap.configRef, and talos-reconcile-job.yaml, which creates the
# object - have to hash the same bytes.
update:
	cp ../kubernetes/files/versions.yaml files/versions.yaml
	cp ../kubernetes/images/kubectl.tag images/kubectl.tag
	$(MAKE) generate

# Worker disks for pools on a Kairos NodeGroupClass, one per Kubernetes minor
# in files/versions.yaml, each with kubelet and kubeadm of that minor's patch.
# images/kairos-worker builds the OS, AuroraBoot turns it into a raw EFI disk
# per architecture, and images/kairos-worker-disk wraps the disks into the
# container disk CDI imports. The ref of each lands in
# images/kairos-worker-<minor>.tag, which the chart reads for its default.
#
# The OS image itself is published as kairos-worker-base-<minor>, pinned in
# images/kairos-worker-base-<minor>.tag, so an operator can build a worker
# image FROM it by digest. When the build pushes, each disk is made from that
# published image pulled back by digest, so the disk and the image an operator
# builds on are the same bytes. A build that does not push makes the disk from
# a local build of the OS and publishes no OS image.
NODES_PKG_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)
KAIROS_WORKER_MINORS := $(shell yq -r 'keys | .[]' files/versions.yaml)
# PLATFORM is empty under LOAD=1, which means the host architecture only.
KAIROS_WORKER_ARCHES := $(patsubst linux/%,%,$(subst $(comma), ,$(or $(PLATFORM),linux/$(patsubst x86_64,amd64,$(patsubst aarch64,arm64,$(shell uname -m))))))
KAIROS_WORKER_OUT := $(abspath ../../../_out/kairos-worker)
AURORABOOT_IMAGE := quay.io/kairos/auroraboot:v0.27.1@sha256:c8c1ef9cdbe42c4a7a9fa97d8ab9f63cb667c8f4e7d9fc0002df5778b69465ff

image: $(addprefix image-kairos-worker-,$(KAIROS_WORKER_MINORS))
	$(if $(KAIROS_WORKER_MINORS),,$(error no Kubernetes minors read from files/versions.yaml))

image-kairos-worker-%:
	rm -rf "$(KAIROS_WORKER_OUT)/$*"
	set -e; for a in $(KAIROS_WORKER_ARCHES); do \
		$(MAKE) kairos-worker-os-test KAIROS_WORKER_MINOR=$* PLATFORM=linux/$$a PUSH=0 LOAD=0; \
	done
	$(if $(or $(filter 1,$(PUSH)),$(strip $(OCI_EXPORT_DIR))),$(MAKE) kairos-worker-base KAIROS_WORKER_MINOR=$*)
	set -e; for a in $(KAIROS_WORKER_ARCHES); do \
		$(MAKE) kairos-worker-raw KAIROS_WORKER_MINOR=$* PLATFORM=linux/$$a PUSH=0 LOAD=1 \
			KAIROS_WORKER_BASE_PULL=$(filter 1,$(PUSH)); \
	done
	docker buildx build "$(KAIROS_WORKER_OUT)/$*" --file images/kairos-worker-disk/Dockerfile \
		$(call image-tags,kairos-worker-$*,$(NODES_PKG_TAG)) \
		--metadata-file "$(KAIROS_WORKER_OUT)/$*/metadata.json" \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/kairos-worker-$*:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' "$(KAIROS_WORKER_OUT)/$*/metadata.json" -o json -r)" \
		> images/kairos-worker-$*.tag
	rm -rf "$(KAIROS_WORKER_OUT)/$*"

# One architecture of one minor. These run in a sub-make whose PLATFORM names
# that single architecture, so $(BUILDX_ARGS) carries it: AuroraBoot needs the
# OS image loaded into the docker store, which cannot hold a multi-platform
# index.
KAIROS_WORKER_K8S = $(shell yq -r '."$(KAIROS_WORKER_MINOR)"' files/versions.yaml)
KAIROS_WORKER_ARCH = $(patsubst linux/%,%,$(PLATFORM))
KAIROS_WORKER_OS = cozystack.local/kairos-worker-os:$(KAIROS_WORKER_MINOR)-$(KAIROS_WORKER_ARCH)
KAIROS_WORKER_RAW = $(KAIROS_WORKER_OUT)/$(KAIROS_WORKER_MINOR)/$(KAIROS_WORKER_ARCH)

kairos-worker-os-test:
	docker buildx build images/kairos-worker --target test \
		--build-arg KUBERNETES_VERSION="$(KAIROS_WORKER_K8S)" \
		--cache-from type=registry,ref=$(CACHE_REGISTRY)/kairos-worker-os-$(KAIROS_WORKER_MINOR):$(CACHE_TAG) \
		$(BUILDX_ARGS)

kairos-worker-base:
	mkdir -p "$(KAIROS_WORKER_OUT)"
	docker buildx build images/kairos-worker \
		--build-arg KUBERNETES_VERSION="$(KAIROS_WORKER_K8S)" \
		$(call image-tags,kairos-worker-base-$(KAIROS_WORKER_MINOR),$(NODES_PKG_TAG)) \
		$(call cache-args,kairos-worker-os-$(KAIROS_WORKER_MINOR)) \
		--metadata-file "$(KAIROS_WORKER_OUT)/base-$(KAIROS_WORKER_MINOR).json" \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/kairos-worker-base-$(KAIROS_WORKER_MINOR):$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' "$(KAIROS_WORKER_OUT)/base-$(KAIROS_WORKER_MINOR).json" -o json -r)" \
		> images/kairos-worker-base-$(KAIROS_WORKER_MINOR).tag
	rm -f "$(KAIROS_WORKER_OUT)/base-$(KAIROS_WORKER_MINOR).json"

kairos-worker-raw:
ifeq ($(KAIROS_WORKER_BASE_PULL),1)
	ref="$$(sed -E 's/:[^/:@]+@/@/' images/kairos-worker-base-$(KAIROS_WORKER_MINOR).tag)"; \
	docker pull --platform "$(PLATFORM)" "$$ref" && \
	docker tag "$$ref" "$(KAIROS_WORKER_OS)" && \
	docker image rm "$$ref" >/dev/null
else
	docker buildx build images/kairos-worker --tag "$(KAIROS_WORKER_OS)" \
		--build-arg KUBERNETES_VERSION="$(KAIROS_WORKER_K8S)" \
		$(call cache-args,kairos-worker-os-$(KAIROS_WORKER_MINOR)) \
		$(BUILDX_ARGS)
endif
	mkdir -p "$(KAIROS_WORKER_RAW)"
	docker run --rm --privileged -v /var/run/docker.sock:/var/run/docker.sock \
		-v "$(KAIROS_WORKER_RAW):/aurora" -v "$(abspath images/kairos-worker/disk.yaml):/config.yaml:ro" \
		$(AURORABOOT_IMAGE) --set arch="$(KAIROS_WORKER_ARCH)" --set disable_http_server=true --set disable_netboot=true \
		--set disk.efi=true --set container_image="docker://$(KAIROS_WORKER_OS)" --set state_dir=/aurora \
		--cloud-config /config.yaml
	docker image rm "$(KAIROS_WORKER_OS)" >/dev/null
	find "$(KAIROS_WORKER_RAW)" -mindepth 1 -maxdepth 1 ! -name '*.raw' -exec rm -rf {} +
