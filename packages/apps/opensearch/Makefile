include ../../../hack/package.mk

.PHONY: generate update

generate:
	cozyvalues-gen -m 'opensearch' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/opensearch/types.go
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	$(MAKE) generate
