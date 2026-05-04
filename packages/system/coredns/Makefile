export NAME=coredns
export NAMESPACE=kube-system

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add cozy-coredns https://coredns.github.io/helm
	helm repo update cozy-coredns
	helm pull cozy-coredns/coredns --untar --untardir charts
	# Drop vendored upstream helm-unittest fixtures: they hard-code
	# upstream defaults (test-release-coredns names, no
	# k8sAppLabelOverride, no service.name override) and fail under
	# the cozystack values.yaml that pins kube-dns. The cozystack-side
	# fixtures in tests/ are the only ones CI should run.
	rm -rf charts/coredns/tests
