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

flux_is_ok() {
  kubectl wait --for=condition=available -n cozy-fluxcd deploy/source-controller deploy/helm-controller --timeout=1s
  kubectl wait --for=condition=ready -n cozy-fluxcd helmrelease/fluxcd --timeout=1s # to call "apply resume" below
}

ensure_fluxcd() {
  if flux_is_ok; then
    return
  fi
  # Install fluxcd-operator
  if kubectl get helmreleases.helm.toolkit.fluxcd.io  -n cozy-fluxcd fluxcd-operator; then
    make -C packages/system/fluxcd-operator apply resume
  else
    make -C packages/system/fluxcd-operator apply-locally
  fi
  wait_for_crds fluxinstances.fluxcd.controlplane.io

   # Install fluxcd
  if kubectl get helmreleases.helm.toolkit.fluxcd.io  -n cozy-fluxcd fluxcd; then
    make -C packages/system/fluxcd apply resume
  else
    make -C packages/system/fluxcd apply-locally
  fi
  wait_for_crds helmreleases.helm.toolkit.fluxcd.io helmrepositories.source.toolkit.fluxcd.io
}

wait_for_crds() {
  timeout 60 sh -c "until kubectl get crd $*; do sleep 1; done"
}

install_basic_charts() {
  if [ "$BUNDLE" = "paas-full" ] || [ "$BUNDLE" = "distro-full" ]; then
  make -C packages/system/cilium apply resume
  fi
  if [ "$BUNDLE" = "paas-full" ]; then
    make -C packages/system/kubeovn apply resume
  fi
}

cd "$(dirname "$0")/.."

# Run migrations
run_migrations

# Install namespaces
make -C packages/core/platform namespaces-apply

# Install fluxcd
ensure_fluxcd

# Install platform chart
make -C packages/core/platform reconcile

# Install basic charts
if ! flux_is_ok; then
  install_basic_charts
fi

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

# Reconcile platform chart
trap 'exit' INT TERM
while true; do
  sleep 60 & wait
  make -C packages/core/platform reconcile
done
