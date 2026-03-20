export NAME=envoy-gateway
export NAMESPACE=cozy-$(NAME)

CHART_VERSION=v1.7.1

include ../../../hack/package.mk

update:
	rm -rf charts
	helm pull oci://docker.io/envoyproxy/gateway-helm --version $(CHART_VERSION) --untar --untardir charts
