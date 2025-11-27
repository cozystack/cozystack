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
	# Metrics-server
	helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
	helm repo update metrics-server
	helm pull metrics-server/metrics-server --untar --untardir charts
	# Fluent-bit
	helm repo add fluent https://fluent.github.io/helm-charts
	helm repo update fluent
	helm pull fluent/fluent-bit --untar --untardir charts
