include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test generate update image

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

# Worker disks for `os: kairos` pools, one per Kubernetes minor in
# files/versions.yaml, each with kubelet and kubeadm of that minor's patch.
# images/kairos-worker builds the OS, AuroraBoot turns it into a raw EFI disk
# per architecture, and images/kairos-worker-disk wraps the disks into the
# container disk CDI imports. The ref of each lands in
# images/kairos-worker-<minor>.tag, which the chart reads for its default.
NODES_PKG_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)
KAIROS_WORKER_MINORS := $(shell yq -r 'keys | .[]' files/versions.yaml)
# PLATFORM is empty under LOAD=1, which means the host architecture only.
KAIROS_WORKER_ARCHES := $(patsubst linux/%,%,$(subst $(comma), ,$(or $(PLATFORM),linux/$(patsubst x86_64,amd64,$(patsubst aarch64,arm64,$(shell uname -m))))))
KAIROS_WORKER_OUT := $(abspath ../../../_out/kairos-worker)
AURORABOOT_IMAGE := quay.io/kairos/auroraboot:v0.27.1@sha256:c8c1ef9cdbe42c4a7a9fa97d8ab9f63cb667c8f4e7d9fc0002df5778b69465ff

image: $(addprefix image-kairos-worker-,$(KAIROS_WORKER_MINORS))
	$(if $(KAIROS_WORKER_MINORS),,$(error no Kubernetes minors read from files/versions.yaml))

image-kairos-worker-%:
	set -e; \
	k8s=$$(yq -r '."$*"' files/versions.yaml); \
	out="$(KAIROS_WORKER_OUT)/$*"; \
	rm -rf "$$out"; \
	for a in $(KAIROS_WORKER_ARCHES); do \
		os="cozystack.local/kairos-worker-os:$*-$$a"; \
		docker buildx build images/kairos-worker --platform "linux/$$a" --target test \
			--build-arg KUBERNETES_VERSION="$$k8s" --build-arg 'GOPROXY=$(IMAGE_GOPROXY)' \
			$(if $(strip $(BUILDER)),--builder=$(BUILDER)) --cache-from type=registry,ref=$(CACHE_REGISTRY)/kairos-worker-os-$*:$(CACHE_TAG); \
		docker buildx build images/kairos-worker --platform "linux/$$a" --load --tag "$$os" \
			--build-arg KUBERNETES_VERSION="$$k8s" --build-arg 'GOPROXY=$(IMAGE_GOPROXY)' \
			$(if $(strip $(BUILDER)),--builder=$(BUILDER)) $(call cache-args,kairos-worker-os-$*); \
		mkdir -p "$$out/$$a"; \
		docker run --rm --privileged -v /var/run/docker.sock:/var/run/docker.sock \
			-v "$$out/$$a:/aurora" -v "$(abspath images/kairos-worker/disk.yaml):/config.yaml:ro" \
			$(AURORABOOT_IMAGE) --set arch="$$a" --set disable_http_server=true --set disable_netboot=true \
			--set disk.efi=true --set container_image="docker://$$os" --set state_dir=/aurora \
			--cloud-config /config.yaml; \
		docker image rm "$$os" >/dev/null; \
		find "$$out/$$a" -mindepth 1 -maxdepth 1 ! -name '*.raw' -exec rm -rf {} +; \
	done; \
	docker buildx build "$$out" --file images/kairos-worker-disk/Dockerfile \
		$(call image-tags,kairos-worker-$*,$(NODES_PKG_TAG)) \
		--metadata-file "$$out/metadata.json" \
		$(BUILDX_ARGS); \
	echo "$(REGISTRY)/kairos-worker-$*:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' "$$out/metadata.json" -o json -r)" \
		> images/kairos-worker-$*.tag; \
	rm -rf "$$out"
