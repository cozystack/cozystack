export NAME=kubevirt
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf templates
	mkdir templates
	export RELEASE=$$(curl https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt) && \
	wget https://github.com/kubevirt/kubevirt/releases/download/$${RELEASE}/kubevirt-cr.yaml -O templates/kubevirt-cr.yaml
	sed -i 's/namespace: kubevirt/namespace: cozy-kubevirt/g' templates/kubevirt-cr.yaml
