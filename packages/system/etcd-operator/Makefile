export NAME=etcd-operator
export NAMESPACE=cozy-${NAME}

include ../../../hack/package.mk

# Pinned ref of github.com/cozystack/etcd-operator to vendor the manager
# ClusterRole from. The Deployment is cozystack-authored (image, cluster
# domain, VPA), so only the RBAC role is re-vendored; refresh templates/rbac.yaml
# by hand from config/rbac/role.yaml at this ref. CRDs live in the sibling
# etcd-operator-crds package.
ETCD_OPERATOR_REF ?= v0.5.2

update:
	@echo "etcd-operator is a cozystack-authored chart; refresh templates/rbac.yaml"
	@echo "from github.com/cozystack/etcd-operator config/rbac/role.yaml @ $(ETCD_OPERATOR_REF)"
	@echo "and re-vendor CRDs via 'make -C ../etcd-operator-crds update'."

test:
	helm unittest .
