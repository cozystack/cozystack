export NAME=victoria-metrics-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	# VictoriaMetrics operator
	helm repo add vm https://victoriametrics.github.io/helm-charts/
	helm repo update vm
	helm pull vm/victoria-metrics-operator --untar --untardir charts
