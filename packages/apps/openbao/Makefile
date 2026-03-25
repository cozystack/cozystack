include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'openbao' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/openbao/types.go
	../../../hack/update-crd.sh
