include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'nats' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/nats/types.go
	../../../hack/update-crd.sh
