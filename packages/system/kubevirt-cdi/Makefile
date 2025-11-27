export NAME=kubevirt-cdi
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf templates
	mkdir templates
	export VERSION=$$(basename $$(curl -s -w %{redirect_url} https://github.com/kubevirt/containerized-data-importer/releases/latest)) && \
	wget https://github.com/kubevirt/containerized-data-importer/releases/download/$$VERSION/cdi-cr.yaml -O templates/cdi-cr.yaml
