export NAME=monitoring-agents
export NAMESPACE=cozy-monitoring

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update prometheus-community
	# Kube-state-metrics
	helm pull prometheus-community/kube-state-metrics --untar --untardir charts
	# Node-exporter
	helm pull prometheus-community/prometheus-node-exporter --untar --untardir charts
	# Fluent-bit
	helm repo add fluent https://fluent.github.io/helm-charts
	helm repo update fluent
	helm pull fluent/fluent-bit --untar --untardir charts
