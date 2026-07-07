export NAME=etcd-operator-crds
export NAMESPACE=cozy-etcd-operator

include ../../../hack/package.mk

# Pinned ref of github.com/cozystack/etcd-operator to vendor CRDs from.
ETCD_OPERATOR_REF ?= v0.5.2

update:
	rm -rf templates
	mkdir templates
	kubectl kustomize "github.com/cozystack/etcd-operator/config/crd?ref=$(ETCD_OPERATOR_REF)" | \
	  yq -s '"templates/" + (.metadata.name | sub("\..*$$"; "")) + ".yaml"'
