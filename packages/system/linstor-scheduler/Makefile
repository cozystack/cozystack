export NAME=linstor-scheduler
export NAMESPACE=cozy-linstor

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add piraeus-charts https://piraeus.io/helm-charts/
	helm repo update piraeus-charts
	helm pull piraeus-charts/linstor-scheduler --untar --untardir charts --version 0.2.5
	patch --no-backup-if-mismatch -p4 < patches/add-scheduler-component-label.patch
