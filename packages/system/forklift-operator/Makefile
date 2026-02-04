export NAME=forklift-operator
export NAMESPACE=cozy-forklift

include ../../../hack/package.mk

update:
	rm -rf templates
	mkdir templates
	export VERSION=v2.10.4 && \
	git clone --depth 1 --branch $$VERSION https://github.com/kubev2v/forklift.git /tmp/forklift-$$VERSION && \
	kubectl kustomize /tmp/forklift-$$VERSION/operator/config/default/ > templates/forklift-operator.yaml && \
	rm -rf /tmp/forklift-$$VERSION
	sed -i 's/namespace: forklift-operator-system/namespace: $(NAMESPACE)/g' templates/forklift-operator.yaml
	python3 -c "import sys; content=open('templates/forklift-operator.yaml').read(); docs=content.split('---'); result=[d for d in docs if 'kind: Namespace' not in d.strip().split(chr(10))[0:5] or not any(l.strip()=='kind: Namespace' for l in d.strip().split(chr(10)))]; open('templates/forklift-operator.yaml','w').write('---'.join([d for d in result if d.strip() and not any(l.strip()=='kind: Namespace' for l in d.strip().split(chr(10)))]))"
