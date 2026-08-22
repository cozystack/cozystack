export NAME=monitoring-agents
export NAMESPACE=cozy-monitoring

include ../../../hack/package.mk

# hack/helm-unit-tests.sh discovers work by probing for this target, so a
# package without one is skipped in silence however many suites sit under
# tests/. This package had suites and no target, so none of them had ever run.
test:
	helm unittest .

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
