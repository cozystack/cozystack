export NAME=kubevirt-operator
export NAMESPACE=cozy-kubevirt

include ../../../hack/package.mk

update:
	mkdir -p templates
	@# A live migration onto the new virt-launcher can fail on virtio feature bits
	@# whenever its QEMU build moves at all, major or not (kubevirt/kubevirt#16386
	@# broke on qemu-kvm 9.1.0-19 to 9.1.0-20): test one before moving RELEASE.
	export RELEASE=v1.9.0 && \
	wget https://github.com/kubevirt/kubevirt/releases/download/$${RELEASE}/kubevirt-operator.yaml -O templates/kubevirt-operator.yaml && \
	sed -i 's/namespace: kubevirt/namespace: $(NAMESPACE)/g' templates/kubevirt-operator.yaml
	awk -i inplace -v RS="---" '!/kind: Namespace/{printf "%s", $$0 RS}' templates/kubevirt-operator.yaml
