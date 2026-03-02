export NAME=linstor-scheduler
export NAMESPACE=cozy-linstor

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add piraeus-charts https://piraeus.io/helm-charts/
	helm repo update piraeus-charts
	helm pull piraeus-charts/linstor-scheduler --untar --untardir charts
	patch --no-backup-if-mismatch -p4 < patches/disable-ca-key-rotation.patch
