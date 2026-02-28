#!/bin/bash
# Migration script from Cozystack ConfigMaps to Package-based configuration
# This script converts cozystack, cozystack-branding, and cozystack-scheduling
# ConfigMaps into a Package resource with the new values structure.

set -e

NAMESPACE="cozy-system"

echo "============================="
echo " Cozystack Migration to v1.0 "
echo "============================="
echo ""
echo "This script will convert existing ConfigMaps to a Package resource."
echo ""

# Check if kubectl is available
if ! command -v kubectl &> /dev/null; then
    echo "Error: kubectl is not installed or not in PATH"
    exit 1
fi

# Check if jq is available
if ! command -v jq &> /dev/null; then
    echo "Error: jq is not installed or not in PATH"
    exit 1
fi

# Check if we can access the cluster
if ! kubectl get namespace "$NAMESPACE" &> /dev/null; then
    echo "Error: Cannot access namespace $NAMESPACE"
    exit 1
fi

# Step 0: Annotate critical resources to prevent Helm from deleting them
echo "Step 0: Protect critical resources from Helm deletion"
echo ""
echo "The following resources will be annotated with helm.sh/resource-policy=keep"
echo "to prevent Helm from deleting them when the installer release is removed:"
echo "  - Namespace: $NAMESPACE"
echo "  - ConfigMap: $NAMESPACE/cozystack-version"
echo ""
read -p "Do you want to annotate these resources? (y/N) " -n 1 -r
echo ""

if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "Annotating namespace $NAMESPACE..."
    kubectl annotate namespace "$NAMESPACE" helm.sh/resource-policy=keep --overwrite
    echo "Annotating ConfigMap cozystack-version..."
    kubectl annotate configmap -n "$NAMESPACE" cozystack-version helm.sh/resource-policy=keep --overwrite 2>/dev/null || echo "  ConfigMap cozystack-version not found, skipping."
    echo ""
    echo "Resources annotated successfully."
else
    echo "WARNING: Skipping annotation. If you remove the Helm installer release,"
    echo "the namespace and its contents may be deleted!"
fi
echo ""

# Read ConfigMap cozystack
echo "Reading ConfigMap cozystack..."
COZYSTACK_CM=$(kubectl get configmap -n "$NAMESPACE" cozystack -o json 2>/dev/null || echo "{}")

# Read ConfigMap cozystack-branding
echo "Reading ConfigMap cozystack-branding..."
BRANDING_CM=$(kubectl get configmap -n "$NAMESPACE" cozystack-branding -o json 2>/dev/null || echo "{}")

# Read ConfigMap cozystack-scheduling
echo "Reading ConfigMap cozystack-scheduling..."
SCHEDULING_CM=$(kubectl get configmap -n "$NAMESPACE" cozystack-scheduling -o json 2>/dev/null || echo "{}")

# Extract values from cozystack ConfigMap
CLUSTER_DOMAIN=$(echo "$COZYSTACK_CM" | jq -r '.data["cluster-domain"] // "cozy.local"')
ROOT_HOST=$(echo "$COZYSTACK_CM" | jq -r '.data["root-host"] // "example.org"')
API_SERVER_ENDPOINT=$(echo "$COZYSTACK_CM" | jq -r '.data["api-server-endpoint"] // ""')
OIDC_ENABLED=$(echo "$COZYSTACK_CM" | jq -r '.data["oidc-enabled"] // "false"')
KEYCLOAK_REDIRECTS=$(echo "$COZYSTACK_CM" | jq -r '.data["extra-keycloak-redirect-uri-for-dashboard"] // ""' )
TELEMETRY_ENABLED=$(echo "$COZYSTACK_CM" | jq -r '.data["telemetry-enabled"] // "true"')
BUNDLE_NAME=$(echo "$COZYSTACK_CM" | jq -r '.data["bundle-name"] // "paas-full"')
BUNDLE_DISABLE=$(echo "$COZYSTACK_CM" | jq -r '.data["bundle-disable"] // ""')
BUNDLE_ENABLE=$(echo "$COZYSTACK_CM" | jq -r '.data["bundle-enable"] // ""')
EXPOSE_INGRESS=$(echo "$COZYSTACK_CM" | jq -r '.data["expose-ingress"] // "tenant-root"')
EXPOSE_SERVICES=$(echo "$COZYSTACK_CM" | jq -r '.data["expose-services"] // ""')

# Certificate issuer configuration (old undocumented field: clusterissuer)
OLD_CLUSTER_ISSUER=$(echo "$COZYSTACK_CM" | jq -r '.data["clusterissuer"] // ""')

# Convert old clusterissuer value to new solver/issuerName fields
SOLVER=""
ISSUER_NAME=""
case "$OLD_CLUSTER_ISSUER" in
    cloudflare)
        SOLVER="dns01"
        ISSUER_NAME="letsencrypt-prod"
        ;;
    http01)
        SOLVER="http01"
        ISSUER_NAME="letsencrypt-prod"
        ;;
    "")
        # Field not set; omit from Package so chart defaults apply
        ;;
    *)
        # Unrecognised value — treat as custom ClusterIssuer name with no solver override
        ISSUER_NAME="$OLD_CLUSTER_ISSUER"
        ;;
esac

# Build certificates YAML block (empty string when no override needed)
if [ -n "$SOLVER" ] || [ -n "$ISSUER_NAME" ]; then
    CERTIFICATES_SECTION="          certificates:
            solver: \"${SOLVER}\"
            issuerName: \"${ISSUER_NAME}\""
else
    CERTIFICATES_SECTION=""
fi

# Network configuration
POD_CIDR=$(echo "$COZYSTACK_CM" | jq -r '.data["ipv4-pod-cidr"] // "10.244.0.0/16"')
POD_GATEWAY=$(echo "$COZYSTACK_CM" | jq -r '.data["ipv4-pod-gateway"] // "10.244.0.1"')
SVC_CIDR=$(echo "$COZYSTACK_CM" | jq -r '.data["ipv4-svc-cidr"] // "10.96.0.0/16"')
JOIN_CIDR=$(echo "$COZYSTACK_CM" | jq -r '.data["ipv4-join-cidr"] // "100.64.0.0/16"')

EXTERNAL_IPS=$(echo "$COZYSTACK_CM" | jq -r '.data["expose-external-ips"] // ""')
if [ -z "$EXTERNAL_IPS" ]; then
    EXTERNAL_IPS="[]"
else
    EXTERNAL_IPS=$(echo "$EXTERNAL_IPS" | sed 's/,/\n/g' | awk 'BEGIN{print}{print "          - "$0}')
fi

# Convert comma-separated lists to YAML arrays
if [ -z "$BUNDLE_DISABLE" ]; then
    DISABLED_PACKAGES="[]"
else
    DISABLED_PACKAGES=$(echo "$BUNDLE_DISABLE" | sed 's/,/\n/g' | awk 'BEGIN{print}{print "          - "$0}')
fi

if [ -z "$BUNDLE_ENABLE" ]; then
    ENABLED_PACKAGES="[]"
else
    ENABLED_PACKAGES=$(echo "$BUNDLE_ENABLE" | sed 's/,/\n/g' | awk 'BEGIN{print}{print "          - "$0}')
fi

if [ -z "$EXPOSE_SERVICES" ]; then
    EXPOSED_SERVICES_YAML="[]"
else
    EXPOSED_SERVICES_YAML=$(echo "$EXPOSE_SERVICES" | sed 's/,/\n/g' | awk 'BEGIN{print}{print "            - "$0}')
fi

# Update bundle naming
BUNDLE_NAME=$(echo "$BUNDLE_NAME" | sed 's/paas/isp/')

# Extract branding if available
BRANDING=$(echo "$BRANDING_CM" | jq -r '.data // {} | to_entries[] | "\(.key): \"\(.value)\""')
if [ -z "$BRANDING" ]; then 
    BRANDING="{}"
else
    BRANDING=$(echo "$BRANDING" | awk 'BEGIN{print}{print "          " $0}')
fi

# Extract scheduling if available
SCHEDULING_CONSTRAINTS=$(echo "$SCHEDULING_CM" | jq -r '.data["globalAppTopologySpreadConstraints"] // ""')
if [ -z "$SCHEDULING_CONSTRAINTS" ]; then
    SCHEDULING_CONSTRAINTS='""'
else
    SCHEDULING_CONSTRAINTS=$(echo "$SCHEDULING_CONSTRAINTS" | awk 'BEGIN{print}{print "            " $0}')
fi

echo ""
echo "Extracted configuration:"
echo "  Cluster Domain: $CLUSTER_DOMAIN"
echo "  Root Host: $ROOT_HOST"
echo "  API Server Endpoint: $API_SERVER_ENDPOINT"
echo "  OIDC Enabled: $OIDC_ENABLED"
echo "  Bundle Name: $BUNDLE_NAME"
echo "  Certificate Solver: ${SOLVER:-http01 (default)}"
echo "  Issuer Name: ${ISSUER_NAME:-letsencrypt-prod (default)}"
echo ""

# Generate Package YAML
PACKAGE_YAML=$(cat <<EOF
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cozystack-platform
  namespace: $NAMESPACE
spec:
  variant: $BUNDLE_NAME
  components:
    platform:
      values:
        bundles:
          disabledPackages: $DISABLED_PACKAGES
          enabledPackages: $ENABLED_PACKAGES
        networking:
          clusterDomain: "$CLUSTER_DOMAIN"
          podCIDR: "$POD_CIDR"
          podGateway: "$POD_GATEWAY"
          serviceCIDR: "$SVC_CIDR"
          joinCIDR: "$JOIN_CIDR"
        publishing:
          host: "$ROOT_HOST"
          ingressName: "$EXPOSE_INGRESS"
          exposedServices: $EXPOSED_SERVICES_YAML
          apiServerEndpoint: "$API_SERVER_ENDPOINT"
          externalIPs: $EXTERNAL_IPS
${CERTIFICATES_SECTION}
        authentication:
          oidc:
            enabled: $OIDC_ENABLED
            keycloakExtraRedirectUri: "$KEYCLOAK_REDIRECTS"
        scheduling:
          globalAppTopologySpreadConstraints: $SCHEDULING_CONSTRAINTS
        branding: $BRANDING
EOF
)

echo "Generated Package resource:"
echo "---"
echo "$PACKAGE_YAML"
echo "..."
echo ""

read -p "Do you want to apply this Package? (y/N) " -n 1 -r
echo ""

if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "Applying Package..."
    echo "$PACKAGE_YAML" | kubectl apply -f -
    echo ""
    echo "Package applied successfully!"
    echo ""
    echo "You can now safely delete the old ConfigMaps after verifying the migration:"
    echo "  kubectl delete configmap -n $NAMESPACE cozystack cozystack-branding cozystack-scheduling"
else
    echo "Package not applied. You can save the output above and apply it manually."
fi

echo ""
echo "All done!"
