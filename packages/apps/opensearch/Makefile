include ../../../hack/package.mk

.PHONY: generate update test

generate:
	cozyvalues-gen -m 'opensearch' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/opensearch/types.go
	../../../hack/update-crd.sh

test:
	helm unittest .

update:
	hack/update-versions.sh
	$(MAKE) generate
