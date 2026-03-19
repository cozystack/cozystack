include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'vminstance' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vminstance/types.go
	yq -i -o json '.properties.fullnameOverride = {"description": "Override the VirtualMachine resource name. Used by vm-adoption-controller to match imported VM names.", "type": "string"}' values.schema.json
	../../../hack/update-crd.sh
