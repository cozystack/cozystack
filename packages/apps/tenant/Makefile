include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'tenant' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/tenant/types.go
	../../../hack/update-crd.sh

test:
	helm unittest .
