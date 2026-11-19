NAME=info

include ../../../hack/package.mk

test:
	helm unittest .

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	yq -o json -i '.properties = {}' values.schema.json
	../../../hack/update-crd.sh
