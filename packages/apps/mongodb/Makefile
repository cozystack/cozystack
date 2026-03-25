include ../../../hack/package.mk

.PHONY: generate update

generate:
	cozyvalues-gen -m 'mongodb' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/mongodb/types.go
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate
