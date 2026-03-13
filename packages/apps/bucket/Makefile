include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'bucket' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/bucket/types.go
	../../../hack/update-crd.sh
