export NAME=bootbox
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	cd charts && \
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/tinkerbell/charts  | awk -F'[/^]' 'END{print $$3}') && \
	curl -sSL https://github.com/tinkerbell/charts/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 2 charts-$${tag#*v}/tinkerbell
	find charts -maxdepth 1 -mindepth 1 ! -name tink -and ! -name smee -exec rm -rf {} \;
	mkdir -p charts/smee/crds
	mv charts/tink/crds/hardware-crd.yaml charts/smee/crds
	rm -rf charts/tink
