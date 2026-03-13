NAME=harbor

include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'harbor' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/harbor/types.go
	../../../hack/update-crd.sh
