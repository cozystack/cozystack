export NAME=gateway-api-crds
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

# Stamp helm.sh/resource-policy: keep on every CustomResourceDefinition so a
# chart uninstall never garbage-collects the CRDs and, with them, every Gateway,
# HTTPRoute, GRPCRoute and ReferenceGrant in the cluster. The experimental
# kustomization also emits a ValidatingAdmissionPolicy and its binding, which
# hold no user data and are left unannotated.
# The stamp goes into the CRD's existing metadata.annotations block; a CRD that
# arrives without one is skipped, and the chart's helm-unittest suite is what
# catches that.
update:
	rm -rf templates
	mkdir templates
	kubectl kustomize "github.com/kubernetes-sigs/gateway-api/config/crd/experimental?ref=v1.5.1" > templates/crds-experimental.yaml
	@set -e; f=templates/crds-experimental.yaml; tmp=$$(mktemp); \
	  awk '/^    helm.sh\/resource-policy: keep$$/{next} /^kind: /{kind=$$2} /^  annotations:$$/ && kind=="CustomResourceDefinition"{print; print "    helm.sh/resource-policy: keep"; next} {print}' "$$f" > "$$tmp"; \
	  cat "$$tmp" > "$$f"; rm -f "$$tmp"
