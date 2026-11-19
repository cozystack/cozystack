export NAME=linstor-scheduler
export NAMESPACE=cozy-linstor

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add piraeus-charts https://piraeus.io/helm-charts/
	helm repo update piraeus-charts
	helm pull piraeus-charts/linstor-scheduler --untar --untardir charts --version 0.3.1
	# Drop the vendored upstream helm-unittest suite: it asserts upstream
	# defaults and fails under the cozystack umbrella values. The cozystack-side
	# tests/ are the source of truth (same policy as packages/system/coredns).
	rm -rf charts/linstor-scheduler/tests
	patch --no-backup-if-mismatch -p4 < patches/cozystack-scheduler-deployment.patch
