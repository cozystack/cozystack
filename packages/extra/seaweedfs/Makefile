NAME=seaweedfs

include ../../../hack/package.mk

test:
	helm unittest .

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh
