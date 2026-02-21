export NAME=kubevirt-operator
export NAMESPACE=cozy-kubevirt

include ../../../hack/package.mk

update:
	rm -rf templates
	mkdir templates
	# v1.7.0 blocked by https://github.com/kubevirt/kubevirt/issues/16386
	export RELEASE=v1.6.3 && \
	wget https://github.com/kubevirt/kubevirt/releases/download/$${RELEASE}/kubevirt-operator.yaml -O templates/kubevirt-operator.yaml && \
	sed -i 's/namespace: kubevirt/namespace: $(NAMESPACE)/g' templates/kubevirt-operator.yaml
	awk -i inplace -v RS="---" '!/kind: Namespace/{printf "%s", $$0 RS}' templates/kubevirt-operator.yaml
