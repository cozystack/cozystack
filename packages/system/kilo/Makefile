export NAME=kilo
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

apply-locally:
	cozypkg apply --plain -n $(NAMESPACE) $(NAME)

update:
	wget https://raw.githubusercontent.com/squat/kilo/refs/heads/main/manifests/crds.yaml -O templates/crds.yaml
	wget https://raw.githubusercontent.com/squat/kilo/refs/heads/main/manifests/kilo-typhoon-flannel.yaml -O templates/kilo.yaml
	sed -i 's|kube-system|cozy-kilo|g' templates/kilo.yaml
	sed -i 's|--compatibility=flannel|--compatibility=cilium|' templates/kilo.yaml
	sed -i '/- --local=false/a \        - --mesh-granularity=full\n        - --service-cidr=10.244.0.0/24\n        - --service-cidr=10.96.0.0/24' templates/kilo.yaml

image:
	docker buildx build images/kilo \
		--tag $(REGISTRY)/kilo:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kilo:latest \
		--cache-to type=inline \
		--metadata-file images/kilo.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/kilo" \
		yq -i '.kilo.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(TAG)@$$(yq e '."containerimage.digest"' images/kilo.json -o json -r) \
		yq -i '.kilo.image.tag = strenv(TAG)' values.yaml
	rm -f images/kilo.json
