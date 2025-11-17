NAME=cozystack-api
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

run-local:
	openssl req -nodes -new -x509 -keyout /tmp/ca.key -out /tmp/ca.crt -subj "/CN=kube-ca"
	openssl req -out /tmp/client.csr -new -newkey rsa:2048 -nodes -keyout /tmp/client.key -subj "/C=US/ST=SomeState/L=L/OU=Dev/CN=development/O=system:masters"
	openssl x509 -req -days 365 -in /tmp/client.csr -CA /tmp/ca.crt -CAkey /tmp/ca.key -set_serial 01 -sha256 -out /tmp/client.crt
	openssl req -out /tmp/apiserver.csr -new -newkey rsa:2048 -nodes -keyout /tmp/apiserver.key -subj "/CN=cozystack-api" -config cozystack-api-openssl.cnf
	openssl x509 -req -days 365 -in /tmp/apiserver.csr -CA /tmp/ca.crt -CAkey /tmp/ca.key -set_serial 01 -sha256 -out /tmp/apiserver.crt -extensions v3_req -extfile cozystack-api-openssl.cnf
	CGO_ENABLED=0 go build -o /tmp/cozystack-api ../../../cmd/cozystack-api/main.go
	/tmp/cozystack-api --client-ca-file /tmp/ca.crt --tls-cert-file /tmp/apiserver.crt --tls-private-key-file /tmp/apiserver.key --secure-port 6443 --kubeconfig $(KUBECONFIG) --authorization-kubeconfig $(KUBECONFIG) --authentication-kubeconfig $(KUBECONFIG)

debug:
	dlv debug ../../../cmd/cozystack-api/main.go -- --client-ca-file /tmp/ca.crt --tls-cert-file /tmp/apiserver.crt --tls-private-key-file /tmp/apiserver.key --secure-port 6443 --kubeconfig $(KUBECONFIG) --authorization-kubeconfig $(KUBECONFIG) --authentication-kubeconfig $(KUBECONFIG)

image: image-cozystack-api

image-cozystack-api:
	docker buildx build -f images/cozystack-api/Dockerfile ../../.. \
		--tag $(REGISTRY)/cozystack-api:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-api:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-api.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cozystack-api:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/cozystack-api.json -o json -r)" \
		yq -i '.cozystackAPI.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-api.json
