#!/bin/bash
# Migration script from Cozystack ConfigMaps to Package-based configuration
# This script converts cozystack, cozystack-branding, and cozystack-scheduling
# ConfigMaps into a Package resource with the new values structure.

set -e

NAMESPACE="cozy-system"

echo "Cozystack Migration to v1.0"
echo "==========================="
echo ""
echo "This script will convert existing ConfigMaps to a Package resource."
echo ""

# Check if kubectl is available
if ! command -v kubectl &> /dev/null; then
    echo "Error: kubectl is not installed or not in PATH"
    exit 1
fi

# Check if we can access the cluster
if ! kubectl get namespace "$NAMESPACE" &> /dev/null; then
    echo "Error: Cannot access namespace $NAMESPACE"
    exit 1
fi

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
TELEMETRY_ENABLED=$(echo "$COZYSTACK_CM" | jq -r '.data["telemetry-enabled"] // "true"')
BUNDLE_NAME=$(echo "$COZYSTACK_CM" | jq -r '.data["bundle-name"] // "paas-full"')

# Network configuration
POD_CIDR=$(echo "$COZYSTACK_CM" | jq -r '.data["ipv4-pod-cidr"] // "10.244.0.0/16"')
POD_GATEWAY=$(echo "$COZYSTACK_CM" | jq -r '.data["ipv4-pod-gateway"] // "10.244.0.1"')
SVC_CIDR=$(echo "$COZYSTACK_CM" | jq -r '.data["ipv4-svc-cidr"] // "10.96.0.0/16"')
JOIN_CIDR=$(echo "$COZYSTACK_CM" | jq -r '.data["ipv4-join-cidr"] // "100.64.0.0/16"')

# Determine bundle type
case "$BUNDLE_NAME" in
    paas-full|distro-full)
        SYSTEM_ENABLED="true"
        SYSTEM_TYPE="full"
        ;;
    paas-hosted|distro-hosted)
        SYSTEM_ENABLED="false"
        SYSTEM_TYPE="hosted"
        ;;
    *)
        SYSTEM_ENABLED="false"
        SYSTEM_TYPE="hosted"
        ;;
esac

# Extract branding if available
DASHBOARD_BRANDING=$(echo "$BRANDING_CM" | jq -r '.data["dashboard"] // "{}"')
KEYCLOAK_BRANDING=$(echo "$BRANDING_CM" | jq -r '.data["keycloak"] // "{}"')

# Extract scheduling if available
SCHEDULING_CONSTRAINTS=$(echo "$SCHEDULING_CM" | jq -r '.data["topologySpreadConstraints"] // "[]"')

echo ""
echo "Extracted configuration:"
echo "  Cluster Domain: $CLUSTER_DOMAIN"
echo "  Root Host: $ROOT_HOST"
echo "  API Server Endpoint: $API_SERVER_ENDPOINT"
echo "  OIDC Enabled: $OIDC_ENABLED"
echo "  Bundle Name: $BUNDLE_NAME"
echo "  System Enabled: $SYSTEM_ENABLED"
echo "  System Type: $SYSTEM_TYPE"
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
          system:
            enabled: $SYSTEM_ENABLED
            type: "$SYSTEM_TYPE"
          iaas:
            enabled: true
          paas:
            enabled: true
          naas:
            enabled: true
        networking:
          clusterDomain: "$CLUSTER_DOMAIN"
          podCIDR: "$POD_CIDR"
          podGateway: "$POD_GATEWAY"
          serviceCIDR: "$SVC_CIDR"
          joinCIDR: "$JOIN_CIDR"
        publishing:
          host: "$ROOT_HOST"
          apiServerEndpoint: "$API_SERVER_ENDPOINT"
        authentication:
          oidc:
            enabled: $OIDC_ENABLED
        scheduling:
          topologySpreadConstraints: $SCHEDULING_CONSTRAINTS
        branding:
          dashboard: $DASHBOARD_BRANDING
          keycloak: $KEYCLOAK_BRANDING
EOF
)

echo "Generated Package resource:"
echo "---"
echo "$PACKAGE_YAML"
echo "---"
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
echo "Migration complete!"
