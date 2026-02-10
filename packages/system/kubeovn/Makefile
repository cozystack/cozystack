export NAME=kubeovn
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts values.yaml Chart.yaml
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/cozystack/kubeovn-chart | awk -F'[/^]' 'END{print $$3}') && \
	curl -sSL https://github.com/cozystack/kubeovn-chart/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 2 kubeovn-chart-$${tag#*v}/chart
