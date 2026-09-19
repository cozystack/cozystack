include ../../../hack/package.mk

.PHONY: test
test:
	helm unittest .

generate:
	cozyvalues-gen -m 'vpn' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vpn/types.go
	../../../hack/update-crd.sh
