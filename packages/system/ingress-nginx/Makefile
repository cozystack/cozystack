NAME=ingress-nginx-system

# Pinned: the controller image is a cozystack rebuild (protobuf exporter) and
# only exists for selected upstream versions, so the chart must not drift ahead
# of the tags published under ghcr.io/cozystack/ingress-nginx-with-protobuf-exporter.
CHART_VERSION=4.11.5

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
	helm repo update ingress-nginx
	helm pull ingress-nginx/ingress-nginx --version $(CHART_VERSION) --untar --untardir charts
	patch --no-backup-if-mismatch -p 3 < patches/add-metrics2.patch
	patch --no-backup-if-mismatch -p4 < patches/disable-ca-key-rotation.patch
	rm -f charts/ingress-nginx/templates/controller-deployment.yaml.orig
	rm -rf charts/ingress-nginx/changelog/

# --with-subchart=false: the vendored chart carries upstream's own suites, which
# assert against upstream default values and fail under the overrides in our
# values.yaml. They are upstream's contract, not ours; run only our own.
test:
	helm unittest --with-subchart=false .
