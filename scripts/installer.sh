#!/bin/sh
set -o pipefail
set -e

BUNDLE=$(set -x; kubectl get configmap -n cozy-system cozystack -o 'go-template={{index .data "bundle-name"}}')
VERSION=$(find scripts/migrations -mindepth 1 -maxdepth 1 -type f | sort -V | awk -F/ 'END {print $NF+1}')

run_migrations() {
  if ! kubectl get configmap -n cozy-system cozystack-version; then
    kubectl create configmap -n cozy-system cozystack-version --from-literal=version="$VERSION" --dry-run=client -o yaml | kubectl create -f-
    return
  fi
  current_version=$(kubectl get configmap -n cozy-system cozystack-version -o jsonpath='{.data.version}') || true
  until [ "$current_version" = "$VERSION" ]; do
    echo "run migration: $current_version --> $VERSION"
    chmod +x scripts/migrations/$current_version
    scripts/migrations/$current_version
    current_version=$(kubectl get configmap -n cozy-system cozystack-version -o jsonpath='{.data.version}')
  done
}

install_flux() {
  if [ "$INSTALL_FLUX" != "true" ]; then
    return
  fi
  make -C packages/core/flux-aio apply
  wait_for_crds helmreleases.helm.toolkit.fluxcd.io helmrepositories.source.toolkit.fluxcd.io
}

wait_for_crds() {
  timeout 60 sh -c "until kubectl get crd $*; do sleep 1; done"
}

cd "$(dirname "$0")/.."

# Run migrations
run_migrations

# Install namespaces
make -C packages/core/platform namespaces-apply

# Install fluxcd
install_flux

# Install fluxcd certificates
./scripts/issue-flux-certificates.sh

# Install platform chart
make -C packages/core/platform reconcile

# Reconcile Helm repositories
kubectl annotate helmrepositories.source.toolkit.fluxcd.io -A -l cozystack.io/repository reconcile.fluxcd.io/requestedAt=$(date +"%Y-%m-%dT%H:%M:%SZ") --overwrite

# Unsuspend all Cozystack managed charts
kubectl get hr -A -o go-template='{{ range .items }}{{ if .spec.suspend }}{{ .spec.chart.spec.sourceRef.namespace }}/{{ .spec.chart.spec.sourceRef.name }} {{ .metadata.namespace }} {{ .metadata.name }}{{ "\n" }}{{ end }}{{ end }}' | while read repo namespace name; do
  case "$repo" in
    cozy-system/cozystack-system|cozy-public/cozystack-extra|cozy-public/cozystack-apps)
      kubectl patch hr -n "$namespace" "$name" -p '{"spec": {"suspend": null}}' --type=merge --field-manager=flux-client-side-apply
      ;;
  esac
done

# Update all Cozystack managed charts to latest version
kubectl get hr -A -l cozystack.io/ui=true --no-headers | awk '{print "kubectl patch helmrelease -n " $1 " " $2 " --type=merge -p '\''{\"spec\":{\"chart\":{\"spec\":{\"version\":\">= 0.0.0-0\"}}}}'\'' "}' | sh -x

# Reconcile platform chart
trap 'exit' INT TERM
while true; do
  sleep 60 & wait
  make -C packages/core/platform reconcile
done
