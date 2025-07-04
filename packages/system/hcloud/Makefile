export NAME=hcloud-ccm
export NAMESPACE=kube-system

include ../../../scripts/package.mk

update:
	rm -rf charts
	mkdir -p charts
	curl -sSL https://github.com/hetznercloud/hcloud-cloud-controller-manager/archive/refs/heads/master.tar.gz/ | \
	tar xzvf - --strip 2 -C charts hcloud-cloud-controller-manager-main/chart
