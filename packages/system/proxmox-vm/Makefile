export NAME=proxmox-vm
export NAMESPACE=cozy-proxmox-vm

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# The kubemox build this package vendors. Until the open upstream pull requests
# land (alperencelik/kubemox#96, #101–#107: namespaced CRDs, VirtualMachineVolume,
# volumeRef, power state and firmware, IPAM ports, NoCloud, podLabels, cloneable
# volumes), the chart comes from the fork that carries them, built by its CI
# from the pskep branch and named after the commit it was built from.
CHART_VERSION=0.7.0-pskep-9e0381c

.PHONY: image image-console-proxy test test-go test-helm

update:
	rm -rf charts
	mkdir -p charts
	helm pull oci://ghcr.io/themoriarti/charts/kubemox \
	  --version $(CHART_VERSION) --untar --untardir charts

# Regenerate files/instancetypes.yaml, the Proxmox sizing of the KubeVirt
# instance type catalog, from the vendored common-instancetypes manifest.
.PHONY: instancetypes
instancetypes:
	./hack/update-instancetypes.sh ../kubevirt-instancetypes/templates/instancetypes.yaml > files/instancetypes.yaml

test: test-helm test-go

test-helm:
	helm unittest .

test-go:
	cd images/console-proxy && go test ./...

image: image-console-proxy

# The aggregated API behind the dashboard's console and power buttons on the
# Proxmox substrate; see images/console-proxy.
image-console-proxy:
	docker buildx build images/console-proxy \
		$(call image-tags,proxmox-console-proxy,v$(COZYSTACK_VERSION)) \
		$(call cache-args,proxmox-console-proxy) \
		--metadata-file images/console-proxy.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/proxmox-console-proxy:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/console-proxy.json -r)" \
		yq -i '.consoleProxy.image = strenv(IMAGE)' values.yaml
	rm -f images/console-proxy.json
