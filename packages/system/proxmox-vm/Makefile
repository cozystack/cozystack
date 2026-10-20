export NAME=proxmox-vm
export NAMESPACE=cozy-proxmox-vm

# The kubemox build this package vendors. Until the open upstream pull requests
# land (alperencelik/kubemox#96, #101–#106: namespaced CRDs, VirtualMachineVolume,
# volumeRef, power state and firmware, IPAM ports, NoCloud, podLabels), the chart
# comes from the fork that carries them, built by its CI from the pskep branch
# and named after the commit it was built from.
CHART_VERSION=0.7.0-pskep-9e0381c

include ../../../hack/package.mk

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
