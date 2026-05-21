include ../../../hack/package.mk
PRESET_ENUM := ["nano","micro","small","medium","large","xlarge","2xlarge"]

test:
	helm unittest .

generate:
	cozyvalues-gen -m 'kafka' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/kafka/types.go
	../../../hack/update-crd.sh
