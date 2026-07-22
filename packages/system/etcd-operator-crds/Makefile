export NAME=etcd-operator-crds
export NAMESPACE=cozy-etcd-operator

include ../../../hack/package.mk

# Pinned ref of github.com/cozystack/etcd-operator to vendor CRDs from.
ETCD_OPERATOR_REF ?= v0.5.3

# controller-gen writes the CRDs to charts/etcd-operator/crd-bases/ upstream (there
# is no config/crd kustomization). Vendor each verbatim and stamp
# helm.sh/resource-policy: keep so a chart uninstall never garbage-collects the
# CRDs and, with them, every EtcdCluster/EtcdMember/EtcdSnapshot in the cluster.
CRDS = etcdclusters etcdmembers etcdsnapshots
update:
	rm -rf templates
	mkdir templates
	@set -e; for c in $(CRDS); do \
	  src=$$(mktemp); \
	  curl -fsSL "https://raw.githubusercontent.com/cozystack/etcd-operator/$(ETCD_OPERATOR_REF)/charts/etcd-operator/crd-bases/etcd-operator.cozystack.io_$$c.yaml" -o "$$src"; \
	  awk '/^    controller-gen.kubebuilder.io\/version:/{print; print "    helm.sh/resource-policy: keep"; next} {print}' "$$src" > templates/$$c.yaml; \
	  rm -f "$$src"; \
	done
