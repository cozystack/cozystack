export NAME=kubevirt-cdi
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# hack/package.mk defines no `test` target and hack/helm-unit-tests.sh only runs
# a package whose `make -n test` resolves, so a tests/ directory without this
# line is collected by nothing and the suite is skipped in silence.
test:
	helm unittest .

# Nothing under templates/ here is purely vendored. The upload-proxy Ingress
# and TLSRoute are ours and no fetch recreates them, so clearing the directory
# loses them outright, which is why the removal is gone: wget -O truncates the
# one file it writes and leaves the rest.
#
# cdi-cr.yaml is ours too, despite being named after an upstream asset: it
# carries Helm templating and RBAC that no fetch reproduces. The fetch below
# still overwrites it, so a CDI bump means diffing it against the upstream
# asset and re-applying whatever is local by hand.
update:
	mkdir -p templates
	export VERSION=$$(basename $$(curl -s -w %{redirect_url} https://github.com/kubevirt/containerized-data-importer/releases/latest)) && \
	wget https://github.com/kubevirt/containerized-data-importer/releases/download/$$VERSION/cdi-cr.yaml -O templates/cdi-cr.yaml

test:
	helm unittest .
