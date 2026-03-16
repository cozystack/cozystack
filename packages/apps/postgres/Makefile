include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'postgresql' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/postgresql/types.go
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate
