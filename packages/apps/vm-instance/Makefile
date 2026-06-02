include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'vminstance' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vminstance/types.go
	../../../hack/update-crd.sh
