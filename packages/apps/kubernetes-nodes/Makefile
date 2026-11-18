include ../../../hack/package.mk

.PHONY: test generate update

test:
	helm unittest .

generate:
	cozyvalues-gen -m 'kubernetesnodes' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/kubernetesnodes/types.go
	../../../hack/update-crd.sh

# Copy the Kubernetes version map (files/versions.yaml, minor -> patch release)
# and the kubectl.tag used by the talos-reconcile Job from the kubernetes
# chart, which owns both. They are copied, not symlinked, so `helm package`
# bundles them; run this after either changes there.
#
# _talosconfigtemplate.tpl is not copied from anywhere: this chart owns the
# worker machine config. The TalosConfigTemplate it renders is content-hash
# named and both consumers in this chart - nodegroup.yaml, which writes
# bootstrap.configRef, and talos-reconcile-job.yaml, which creates the
# object - have to hash the same bytes.
update:
	cp ../kubernetes/files/versions.yaml files/versions.yaml
	cp ../kubernetes/images/kubectl.tag images/kubectl.tag
	$(MAKE) generate
