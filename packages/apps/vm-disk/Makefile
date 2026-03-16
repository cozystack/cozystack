include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'vmdisk' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vmdisk/types.go
	../../../hack/update-crd.sh
