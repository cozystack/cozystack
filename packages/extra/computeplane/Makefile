NAME=computeplane

include ../../../hack/package.mk

.PHONY: test
test:
	helm unittest .

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh
