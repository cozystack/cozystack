export NAME=snapshot-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add piraeus-charts https://piraeus.io/helm-charts/
	helm repo update piraeus-charts
	helm pull piraeus-charts/snapshot-controller --untar --untardir charts
	# Leave CRDs owned outside this release out of the manifest.
	patch --no-backup-if-mismatch -p1 -d charts/snapshot-controller < patches/skip-foreign-crds.diff

test:
	helm unittest .
