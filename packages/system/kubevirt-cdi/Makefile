export NAME=kubevirt-cdi
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# hack/package.mk defines no `test` target and hack/helm-unit-tests.sh only runs
# a package whose `make -n test` resolves, so a tests/ directory without this
# line is collected by nothing and the suite is skipped in silence.
test:
	helm unittest .

update:
	rm -rf templates
	mkdir templates
	export VERSION=$$(basename $$(curl -s -w %{redirect_url} https://github.com/kubevirt/containerized-data-importer/releases/latest)) && \
	wget https://github.com/kubevirt/containerized-data-importer/releases/download/$$VERSION/cdi-cr.yaml -O templates/cdi-cr.yaml
