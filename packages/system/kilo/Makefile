NAME=kilo
NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

apply-locally:
	cozypkg apply --plain -n $(NAMESPACE) $(NAME)

update:
	wget https://raw.githubusercontent.com/squat/kilo/refs/heads/main/manifests/crds.yaml -O templates/crds.yaml
	wget https://raw.githubusercontent.com/squat/kilo/refs/heads/main/manifests/kilo-typhoon-flannel.yaml -O templates/kilo.yaml
	sed -i 's|kube-system|cozy-kilo|g' templates/kilo.yaml
	sed -i 's|--compatibility=flannel|--compatibility=cilium|' templates/kilo.yaml
	sed -i '/- --local=false/a \        - --mesh-granularity=full\n        - --service-cidr=10.244.0.0/24\n        - --service-cidr=10.96.0.0/24' templates/kilo.yaml
