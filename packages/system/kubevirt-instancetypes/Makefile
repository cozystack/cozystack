export NAME=kubevirt-instancetypes
export NAMESPACE=cozy-kubevirt

include ../../../hack/package.mk

# Pin common-instancetypes for reproducible regeneration. Building from an
# unpinned default branch silently churns the catalog on every `make update`.
# This is a commit SHA rather than a release tag on purpose: the multi-arch
# preferredArchitecture / requirements.architecture fields (KubeVirt 1.8) only
# exist on main past the latest tag (v1.6.0), so a tag would regress them.
# Bump COMMON_INSTANCETYPES_REF deliberately and review the resulting diff.
COMMON_INSTANCETYPES_REF ?= 3a552da6065aebb7900b1c63b2f477c2ec9ed687

update:
	rm -rf templates
	mkdir templates
	kustomize build https://github.com/kubevirt/common-instancetypes/preferences?ref=$(COMMON_INSTANCETYPES_REF) > templates/preferences.yaml
	yq -i 'select(.kind != "VirtualMachinePreference")' templates/preferences.yaml
	# TEMPORARY (see #3005): drop persistent EFI/TPM state. With persistence on,
	# KubeVirt creates a backend-storage PVC that is RWO on the default StorageClass,
	# which pins the VM to its node and blocks live-migration / drain. Use `del` (not
	# `sed -d`) so an emptied preferredTPM stays `{}` rather than null, which the
	# KubeVirt v1.8 CRD structural schema rejects. Revert once vm-state can request RWX.
	yq -i 'del(.spec.firmware.preferredEfi.persistent) | del(.spec.devices.preferredTPM.persistent)' templates/preferences.yaml
	kustomize build https://github.com/kubevirt/common-instancetypes/instancetypes?ref=$(COMMON_INSTANCETYPES_REF) > templates/instancetypes.yaml
	yq -i 'select(.kind != "VirtualMachineInstancetype")' templates/instancetypes.yaml
	# Re-append Cozystack-local instancetypes the pinned catalog no longer ships
	# but existing workloads still reference (see extra-instancetypes.yaml).
	cp extra-instancetypes.yaml templates/extra-instancetypes.yaml
