include ../../../hack/package.mk

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	OUT=../../system/virtualprivatecloud-rd/cozyrds/virtualprivatecloud-cozyplane.yaml VARIANT=cozyplane ../../../hack/update-crd.sh

test:
	helm unittest .

update:
	echo
