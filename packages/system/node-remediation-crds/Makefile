export NAME=node-remediation-crds
export NAMESPACE=cozy-node-remediation

include ../../../hack/package.mk

# The CRDs under templates/ are written by `make -C ../node-remediation update`
# from the same patched upstream trees the operator images are built from, the
# way cert-manager refills cert-manager-crds. The namespace and the admission
# policy next to them are this chart's own.
.PHONY: test
test:
	helm unittest .
