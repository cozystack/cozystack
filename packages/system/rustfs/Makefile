export NAME=rustfs-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	helm repo add rustfs https://charts.rustfs.com
	helm repo update rustfs
	helm pull rustfs/rustfs --untar --untardir charts
