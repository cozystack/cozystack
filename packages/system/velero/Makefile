export NAME=velero
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	# Velero
	helm repo add tanzu https://vmware-tanzu.github.io/helm-charts
	helm repo update tanzu
	helm pull tanzu/velero --untar --untardir charts --version 12.0.3
	# Render a startupProbe from .Values.startupProbe (upstream chart omits it),
	# so a slow server start under heavy parallel install does not trip liveness.
	patch --no-backup-if-mismatch -p4 < patches/add-startup-probe.patch
