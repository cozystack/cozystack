export NAME=multus
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf templates
	mkdir templates
	$(eval RELEASE := $(shell curl -s https://api.github.com/repos/k8snetworkplumbingwg/multus-cni/releases/latest?per_page=1 | jq -r '.name'))
	wget -q https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/refs/tags/$(RELEASE)/deployments/multus-daemonset-thick.yml -O templates/multus-daemonset-thick.yml
	sed -i '/multus-cni/s/snapshot-thick/$(RELEASE)-thick/' templates/multus-daemonset-thick.yml && \
	patch --no-backup-if-mismatch -p4 < patches/customize-deployment.patch
