export NAME=vertical-pod-autoscaler
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

# Stamp helm.sh/resource-policy: keep on every CustomResourceDefinition so a
# chart uninstall never garbage-collects the CRDs and, with them, every
# VerticalPodAutoscaler and VerticalPodAutoscalerCheckpoint in the cluster.
# The stamp goes into the CRD's existing metadata.annotations block; a CRD that
# arrives without one is skipped, and the chart's helm-unittest suite is what
# catches that.
update:
	curl -o ./templates/vpa-v1-crd-gen.yaml https://raw.githubusercontent.com/kubernetes/autoscaler/refs/heads/master/vertical-pod-autoscaler/deploy/vpa-v1-crd-gen.yaml
	@set -e; f=./templates/vpa-v1-crd-gen.yaml; tmp=$$(mktemp); \
	  awk '/^    helm.sh\/resource-policy: keep$$/{next} /^kind: /{kind=$$2} /^  annotations:$$/ && kind=="CustomResourceDefinition"{print; print "    helm.sh/resource-policy: keep"; next} {print}' "$$f" > "$$tmp"; \
	  cat "$$tmp" > "$$f"; rm -f "$$tmp"
