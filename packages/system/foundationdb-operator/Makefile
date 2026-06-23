export NAME=foundationdb-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts tmp-repo
	git clone --depth 1 --branch v2.30.0 https://github.com/FoundationDB/fdb-kubernetes-operator.git tmp-repo
	mkdir -p charts
	cp -r tmp-repo/charts/fdb-operator charts/
	# Remove symlinked CRDs and replace with actual files
	rm -f charts/fdb-operator/crds/apps.foundationdb.org_foundationdbbackups.yaml
	rm -f charts/fdb-operator/crds/apps.foundationdb.org_foundationdbclusters.yaml
	rm -f charts/fdb-operator/crds/apps.foundationdb.org_foundationdbrestores.yaml
	cp tmp-repo/config/crd/bases/apps.foundationdb.org_foundationdbbackups.yaml charts/fdb-operator/crds/
	cp tmp-repo/config/crd/bases/apps.foundationdb.org_foundationdbclusters.yaml charts/fdb-operator/crds/
	cp tmp-repo/config/crd/bases/apps.foundationdb.org_foundationdbrestores.yaml charts/fdb-operator/crds/
	rm -rf tmp-repo
	rm -rf charts/fdb-operator/charts