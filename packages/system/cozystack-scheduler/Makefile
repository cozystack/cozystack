export NAME=cozystack-scheduler
export NAMESPACE=kube-system

include ../../../hack/package.mk

update:
	rm -rf crds templates values.yaml Chart.yaml
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/cozystack/cozystack-scheduler | awk -F'[/^]' 'END{print $$3}') && \
	curl -sSL https://github.com/cozystack/cozystack-scheduler/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 2 cozystack-scheduler-$${tag#*v}/chart
