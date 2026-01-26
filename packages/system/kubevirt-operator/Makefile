export NAME=kubevirt-operator
export NAMESPACE=cozy-kubevirt

include ../../../hack/package.mk

update:
	rm -rf templates
	mkdir templates
	export RELEASE=$$(curl https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt) && \
	wget https://github.com/kubevirt/kubevirt/releases/download/$${RELEASE}/kubevirt-operator.yaml -O templates/kubevirt-operator.yaml && \
	sed -i 's/namespace: kubevirt/namespace: $(NAMESPACE)/g' templates/kubevirt-operator.yaml
	awk -i inplace -v RS="---" '!/kind: Namespace/{printf "%s", $$0 RS}' templates/kubevirt-operator.yaml
