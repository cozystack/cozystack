include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'qdrant' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/qdrant/types.go
	../../../hack/update-crd.sh
