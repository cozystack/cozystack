export NAME=multus
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf templates
	mkdir templates
	export RELEASE=$$(curl -s https://api.github.com/repos/k8snetworkplumbingwg/multus-cni/releases/latest?per_page=1 | jq -r '.name') && \
	wget -q https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/refs/tags/$${RELEASE}/deployments/multus-daemonset-thick.yml -O templates/multus-daemonset-thick.yml
	sed -i 's/namespace: kube-system/namespace: $(NAMESPACE)/g' templates/multus-daemonset-thick.yml
