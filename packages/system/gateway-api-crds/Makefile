export NAME=gateway-api-crds
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf templates
	mkdir templates
	kubectl kustomize "github.com/kubernetes-sigs/gateway-api/config/crd/experimental?ref=v1.2.0" > templates/crds-experimental.yaml
