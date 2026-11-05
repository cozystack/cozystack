include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'vpc' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vpc/types.go
	../../../hack/update-crd.sh

update:
	echo

test:
	helm unittest .
