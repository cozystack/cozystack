include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test generate update

test:
	helm unittest .

generate:
	cozyvalues-gen -m 'kubernetes-nodes' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/kubernetes-nodes/types.go
	../../../hack/update-crd.sh
