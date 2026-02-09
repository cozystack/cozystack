include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add qdrant https://qdrant.github.io/qdrant-helm
	helm repo update qdrant
	helm pull qdrant/qdrant --untar --untardir charts
