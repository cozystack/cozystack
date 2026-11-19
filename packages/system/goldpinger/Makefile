include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add goldpinger https://bloomberg.github.io/goldpinger
	helm repo update goldpinger
	helm pull goldpinger/goldpinger --untar --untardir charts
