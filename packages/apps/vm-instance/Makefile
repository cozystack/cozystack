include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'vminstance' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vminstance/types.go
	#INSTANCE_TYPES=$$(yq e '.metadata.name' -o=json -r ../../system/kubevirt-instancetypes/templates/instancetypes.yaml | yq 'split(" ") | . + [""]' -o json) \
	#  && yq -i -o json ".properties.instanceType.enum = $${INSTANCE_TYPES}" values.schema.json
	# Union the upstream preference catalog with the Cozystack-local deprecated
	# aliases in extra-instancetypes.yaml, so retained EOL profiles (e.g.
	# centos.7*/centos.stream8*) stay in the enum and the dynamic CRD does not
	# reject existing VMInstance CRs that reference them.
	PREFERENCES=$$( { yq e '.metadata.name' ../../system/kubevirt-instancetypes/templates/preferences.yaml; \
	                  yq e 'select(.kind == "VirtualMachineClusterPreference") | .metadata.name' ../../system/kubevirt-instancetypes/templates/extra-instancetypes.yaml; } \
	                | grep -E -v -- '^(---)?$$' | sort -u | jq -R . | jq -s '. + [""]' ) \
	  && yq -i -o json ".properties.instanceProfile.enum = $${PREFERENCES}" values.schema.json
	../../../hack/update-crd.sh
