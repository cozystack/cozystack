include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'tcpbalancer' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/tcpbalancer/types.go
	../../../hack/update-crd.sh
