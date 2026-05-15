export NAME=prometheus-operator-crds
export NAMESPACE=cozy-victoria-metrics-operator

include ../../../hack/package.mk

update:
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update prometheus-community
	helm pull prometheus-community/prometheus-operator-crds --version 25.0.0 --untar --untardir charts
