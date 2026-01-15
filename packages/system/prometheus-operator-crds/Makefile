export NAME=prometheus-operator-crds
export NAMESPACE=cozy-victoria-metrics-operator

include ../../../hack/package.mk

update:
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update prometheus-community
	helm pull prometheus-community/prometheus-operator-crds --untar --untardir charts
	rm -f -- `find charts/prometheus-operator-crds/charts/crds/templates -maxdepth 1 -mindepth 1 | grep -v 'servicemonitor\|podmonitor\|prometheusrule\|probe'`
