include ../../../hack/package.mk

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json
	../../../hack/update-crd.sh

update:
	echo
