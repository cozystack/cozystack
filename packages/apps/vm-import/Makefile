include ../../../hack/package.mk

.PHONY: generate

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh
