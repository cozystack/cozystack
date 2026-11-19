NAME=monitoring

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh

test:
	helm unittest .
