include ../../../scripts/package.mk

.PHONY: generate update

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate
