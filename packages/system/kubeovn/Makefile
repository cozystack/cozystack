KUBEOVN_TAG=v0.40.0

export NAME=kubeovn
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts values.yaml Chart.yaml
	tag=$(KUBEOVN_TAG) && \
	curl -sSL https://github.com/cozystack/kubeovn/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 2 kubeovn-$${tag#*v}/chart
