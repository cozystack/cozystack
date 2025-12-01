include ../../../hack/package.mk
PRESET_ENUM := ["nano","micro","small","medium","large","xlarge","2xlarge"]

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh
