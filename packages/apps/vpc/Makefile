include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'vpc' -v values.yaml -s values.schema.json -g ../../../api/apps/v1alpha1/vpc/types.go
	../../../hack/update-crd.sh

update:
	echo
