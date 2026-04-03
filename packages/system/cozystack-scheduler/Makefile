export NAME=cozystack-scheduler
export NAMESPACE=kube-system

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/cozystack/cozystack-scheduler | awk -F'[/^]' 'END{print $$3}') && \
	mkdir -p charts/cozystack-scheduler && \
	curl -sSL https://github.com/cozystack/cozystack-scheduler/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 2 -C charts/cozystack-scheduler cozystack-scheduler-$${tag#*v}/chart
