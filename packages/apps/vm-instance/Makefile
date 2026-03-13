include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'vminstance' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vminstance/types.go
	#INSTANCE_TYPES=$$(yq e '.metadata.name' -o=json -r ../../system/kubevirt-instancetypes/templates/instancetypes.yaml | yq 'split(" ") | . + [""]' -o json) \
	#  && yq -i -o json ".properties.instanceType.enum = $${INSTANCE_TYPES}" values.schema.json
	PREFERENCES=$$(yq e '.metadata.name' -o=json -r ../../system/kubevirt-instancetypes/templates/preferences.yaml | yq 'split(" ") | . + [""]' -o json) \
	  && yq -i -o json ".properties.instanceProfile.enum = $${PREFERENCES}" values.schema.json
	../../../hack/update-crd.sh
