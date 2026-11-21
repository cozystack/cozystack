# shellcheck shell=sh
# Shared helper for applying the CredentialTemplate CRD during migrations.
#
# The CRD ships with cozystack-controller, whose release can upgrade minutes
# after the tenant releases switch charts. A converted chart that renders a
# CredentialTemplate, or a field the installed CRD does not have yet, fails
# (or has the field pruned) in between, so a conversion that needs the CRD
# applies the copy baked into the migrations image before any chart
# switches. It is labelled and annotated as owned by the cozystack-controller
# release, as that release's later upgrade expects to find it.
#
# Sourced, not executed. A migration that needs it does:
#   . "$(dirname "$0")/lib/credential-crd.sh"
#   apply_credential_crd

# apply_credential_crd
# Server-side apply the baked CRD, hand it to the cozystack-controller Helm
# release, and wait until it is established. $CREDENTIAL_CRD_FILE overrides
# the path, for the tests. Every step fails the migration on error, under the
# caller's set -e.
apply_credential_crd() {
  _crd_file="${CREDENTIAL_CRD_FILE:-/credential-crds/cozystack.io_credentialtemplates.yaml}"
  kubectl apply --server-side --force-conflicts --filename "$_crd_file"
  kubectl label crd credentialtemplates.cozystack.io app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate crd credentialtemplates.cozystack.io \
    meta.helm.sh/release-name=cozystack-controller \
    meta.helm.sh/release-namespace=cozy-system --overwrite
  kubectl wait --for=condition=established crd/credentialtemplates.cozystack.io --timeout=60s
}
