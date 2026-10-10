NAME=proxmox-network
NAMESPACE=cozy-proxmox-network

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-proxmox-network-controller

image-proxmox-network-controller:
	docker buildx build -f images/proxmox-network-controller/Dockerfile ../../.. \
		$(call image-tags,proxmox-network-controller,v$(COZYSTACK_VERSION)) \
		$(call cache-args,proxmox-network-controller) \
		--metadata-file images/proxmox-network-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/proxmox-network-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/proxmox-network-controller.json -o json -r)" \
		yq -i '.proxmoxNetworkController.image = strenv(IMAGE)' values.yaml
	rm -f images/proxmox-network-controller.json
