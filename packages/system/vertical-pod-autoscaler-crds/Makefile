export NAME=vertical-pod-autoscaler
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	curl -o ./templates/vpa-v1-crd-gen.yaml https://raw.githubusercontent.com/kubernetes/autoscaler/refs/heads/master/vertical-pod-autoscaler/deploy/vpa-v1-crd-gen.yaml
