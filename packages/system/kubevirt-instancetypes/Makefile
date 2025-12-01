export NAME=kubevirt-instancetypes
export NAMESPACE=cozy-kubevirt

include ../../../hack/package.mk

update:
	rm -rf templates
	mkdir templates
	kustomize build https://github.com/kubevirt/common-instancetypes/preferences > templates/preferences.yaml
	yq -i 'select(.kind != "VirtualMachinePreference")' templates/preferences.yaml
	kustomize build https://github.com/kubevirt/common-instancetypes/instancetypes > templates/instancetypes.yaml
	yq -i 'select(.kind != "VirtualMachineInstancetype")' templates/instancetypes.yaml
	sed -i '/persistent: true/d' templates/preferences.yaml
