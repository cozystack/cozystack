export NAME=hetzner-ccm
export NAMESPACE=kube-system

include ../../../scripts/package.mk

update:
	rm -rf charts
	helm repo add hcloud https://charts.hetzner.cloud
	helm repo update hcloud
	helm pull hcloud/hcloud-cloud-controller-manager --untar --untardir charts
