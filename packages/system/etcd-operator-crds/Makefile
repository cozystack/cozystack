export NAME=etcd-operator-crds
export NAMESPACE=cozy-etcd-operator

include ../../../hack/package.mk

test:
	helm unittest .

# Pinned ref of github.com/cozystack/etcd-operator to vendor CRDs from.
ETCD_OPERATOR_REF ?= v0.5.4

# controller-gen writes the CRDs to charts/etcd-operator/crd-bases/ upstream (there
# is no config/crd kustomization). Vendor each verbatim and stamp
# helm.sh/resource-policy: keep so a chart uninstall never garbage-collects the
# CRDs and, with them, every EtcdCluster/EtcdMember/EtcdSnapshot in the cluster.
# The stamp is anchored on a line upstream controls, so count the result: one
# annotation per CRD, or the update fails. templates/ is replaced only after
# every CRD has been counted, so a failure leaves the vendored files as they
# were.
CRDS = etcdclusters etcdmembers etcdsnapshots
update:
	@set -e; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	for c in $(CRDS); do \
	  curl -fsSL "https://raw.githubusercontent.com/cozystack/etcd-operator/$(ETCD_OPERATOR_REF)/charts/etcd-operator/crd-bases/etcd-operator.cozystack.io_$$c.yaml" -o "$$tmp/$$c.src"; \
	  awk '/^    controller-gen.kubebuilder.io\/version:/{print; print "    helm.sh/resource-policy: keep"; next} {print}' "$$tmp/$$c.src" > "$$tmp/$$c.yaml"; \
	  n=$$(grep -c '^    helm.sh/resource-policy: keep$$' "$$tmp/$$c.yaml" || true); \
	  [ "$$n" -eq 1 ] || { echo "$$c: stamped $$n keep annotations, expected 1" >&2; exit 1; }; \
	done; \
	rm -rf templates; mkdir templates; \
	for c in $(CRDS); do mv "$$tmp/$$c.yaml" templates/$$c.yaml; done
