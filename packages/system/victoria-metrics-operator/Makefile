export NAME=victoria-metrics-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	# VictoriaMetrics operator
	helm repo add vm https://victoriametrics.github.io/helm-charts/
	helm repo update vm
	helm pull vm/victoria-metrics-operator --untar --untardir charts
	patch --no-backup-if-mismatch -p4 < patches/disable-ca-key-rotation.patch
