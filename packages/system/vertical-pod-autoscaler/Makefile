export NAME=vertical-pod-autoscaler
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/package.mk

update:
	rm -rf charts
	# VirtualPodAutoscaler operator
	helm repo add cowboysysop https://cowboysysop.github.io/charts/
	helm repo update cowboysysop
	helm pull cowboysysop/vertical-pod-autoscaler --untar --untardir charts
