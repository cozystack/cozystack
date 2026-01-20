export NAME=local-ccm
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/cozystack/local-ccm | awk -F'[/^]' 'END{print $$3}') && \
	if [ -z "$$tag" ]; then \
		curl -sSL https://github.com/cozystack/local-ccm/archive/refs/heads/main.tar.gz | \
		tar xzvf - --strip 1 local-ccm-main/charts; \
	else \
		curl -sSL https://github.com/cozystack/local-ccm/archive/refs/tags/$${tag}.tar.gz | \
		tar xzvf - --strip 1 local-ccm-$${tag#*v}/charts; \
	fi
