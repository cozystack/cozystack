NAME=cozy-proxy
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/cozystack/cozy-proxy | awk -F'[/^]' 'END{print $$3}') && \
	curl -sSL https://github.com/cozystack/cozy-proxy/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 1 cozy-proxy-$${tag#*v}/charts
