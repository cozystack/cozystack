#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DASHBOARD_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$SCRIPT_DIR"

GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

log() { echo -e "${CYAN}==>${NC} $*"; }

# Verify kubectl access
log "Using kubectl context: $(kubectl config current-context)"
if ! kubectl api-resources --api-group=core.cozystack.io &>/dev/null; then
  echo "ERROR: Cluster does not have core.cozystack.io API group." >&2
  echo "Set KUBECONFIG to a cluster with Cozystack installed, e.g.:" >&2
  echo "  KUBECONFIG=/path/to/kubeconfig $0" >&2
  exit 1
fi

# Cleanup on exit
cleanup() {
  echo ""
  log "Stopping all services..."
  kill 0 2>/dev/null
}
trap cleanup EXIT

# --- Clone repositories ---
clone_repo() {
  local url="$1" dir="$2" branch="${3:-}"
  if [ ! -d "$dir/.git" ]; then
    log "Cloning $dir..."
    git clone "$url" "$dir"
  fi
  if [ -n "$branch" ]; then
    git -C "$dir" checkout "$branch" 2>/dev/null || true
  fi
}

log "Preparing repositories..."
clone_repo git@github.com:PRO-Robotech/openapi-ui.git openapi-ui release/1.3.0
clone_repo git@github.com:PRO-Robotech/openapi-ui-k8s-bff.git openapi-ui-k8s-bff release/1.3.0
clone_repo git@github.com:PRO-Robotech/openapi-k8s-toolkit.git openapi-k8s-toolkit release/1.3.0

# --- Install dependencies ---
log "Installing dependencies..."
for dir in openapi-ui openapi-ui-k8s-bff openapi-k8s-toolkit; do
  if [ ! -d "$dir/node_modules" ]; then
    log "  npm install in $dir"
    (cd "$dir" && npm install --silent)
  fi
done

# --- Build patched toolkit ---
PATCHES_DIR="$DASHBOARD_DIR/images/openapi-ui/openapi-k8s-toolkit/patches"
UI_TOOLKIT_DIST="openapi-ui/node_modules/@prorobotech/openapi-k8s-toolkit/dist"

if [ ! -f .toolkit-patched ]; then
  log "Building patched openapi-k8s-toolkit..."
  (
    cd openapi-k8s-toolkit
    git checkout -- . 2>/dev/null || true
    for patch in "$PATCHES_DIR"/*.diff; do
      [ -f "$patch" ] && git apply "$patch" && log "  Applied $(basename "$patch")"
    done
    npm run build --silent
  )
  log "Replacing toolkit in openapi-ui node_modules..."
  rm -rf "$UI_TOOLKIT_DIST"
  cp -r openapi-k8s-toolkit/dist "$UI_TOOLKIT_DIST"
  touch .toolkit-patched
fi

# --- Prepare UI ---
mkdir -p openapi-ui/public

if [ ! -f openapi-ui/.env.options ]; then
  log "Creating openapi-ui/.env.options..."
  sed 's|^KUBE_API_URL=.*|KUBE_API_URL=http://localhost:8080|;s|^BFF_URL=.*|BFF_URL=http://localhost:4002|' \
    openapi-ui/.env.options.dist > openapi-ui/.env.options
fi

if [ ! -f openapi-ui/public/env.js ]; then
  log "Generating openapi-ui/public/env.js..."
  cat > openapi-ui/public/env.js <<'ENVJS'
window._env_ = {
      BASEPREFIX: "/openapi-ui",
      HIDE_INSIDE: "true",
      CUSTOMIZATION_API_GROUP: "dashboard.cozystack.io",
      CUSTOMIZATION_API_VERSION: "v1alpha1",
      CUSTOMIZATION_NAVIGATION_RESOURCE: "navigation",
      CUSTOMIZATION_NAVIGATION_RESOURCE_NAME: "navigations",
      INSTANCES_API_GROUP: "dashboard.cozystack.io",
      INSTANCES_RESOURCE_NAME: "instances",
      INSTANCES_VERSION: "v1alpha1",
      MARKETPLACE_GROUP: "dashboard.cozystack.io",
      MARKETPLACE_KIND: "MarketplacePanel",
      MARKETPLACE_RESOURCE_NAME: "marketplacepanels",
      MARKETPLACE_VERSION: "v1alpha1",
      NAVIGATE_FROM_CLUSTERLIST: "/openapi-ui/~recordValue~/api-table/core.cozystack.io/v1alpha1/tenantnamespaces",
      PROJECTS_API_GROUP: "core.cozystack.io",
      PROJECTS_RESOURCE_NAME: "tenantnamespaces",
      PROJECTS_VERSION: "v1alpha1",
      CUSTOM_NAMESPACE_API_RESOURCE_API_GROUP: "core.cozystack.io",
      CUSTOM_NAMESPACE_API_RESOURCE_API_VERSION: "v1alpha1",
      CUSTOM_NAMESPACE_API_RESOURCE_RESOURCE_NAME: "tenantnamespaces",
      USE_NAMESPACE_NAV: "true",
      TITLE_TEXT: "Cozystack Dashboard",
      FOOTER_TEXT: "Cozystack",
      CUSTOM_TENANT_TEXT: "dev",
      THEME_TOKENS_COLORS_DARK: {
        "colorText": "rgba(232, 236, 244, 1)",
        "colorTextSecondary": "rgba(167, 177, 196, 1)",
        "colorTextDisabled": "rgba(109, 119, 136, 1)",
        "colorTextTertiary": "rgba(15, 17, 21, 1)",
        "colorBgLayout": "rgba(15, 17, 21, 1)",
        "colorBgContainer": "rgba(23, 26, 32, 1)",
        "colorBgSpotlight": "rgba(30, 34, 43, 1)",
        "colorBgElevated": "rgba(35, 40, 50, 1)",
        "colorBorder": "rgba(42, 49, 60, 1)",
        "colorBgMask": "rgba(0, 0, 0, 0.55)",
        "colorPrimaryHover": "rgba(37, 108, 219, 1)",
        "colorPrimary": "rgba(58, 134, 255, 1)",
        "colorPrimaryActive": "rgba(27, 86, 178, 1)",
        "colorPrimaryBg": "rgba(54, 134, 255, 0.13)",
        "colorSuccess": "rgba(61, 209, 138, 1)",
        "colorWarning": "rgba(245, 165, 36, 1)",
        "colorError": "rgba(240, 81, 77, 1)",
        "colorInfo": "rgba(108, 166, 255, 1)"
      },
      THEME_TOKENS_SIZES: {"borderRadius": 6},
      THEME_TOKENS_COMPONENTS_LIGHT: {"Layout": {}},
      THEME_TOKENS_COMPONENTS_DARK: {"Layout": {}},
      THEME_TOKENS_USE_MERGE_STRATEGY: 'false'
    }
ENVJS
fi

# --- Prepare BFF ---
if [ ! -f openapi-ui-k8s-bff/.env ]; then
  log "Generating openapi-ui-k8s-bff/.env..."
  cat > openapi-ui-k8s-bff/.env <<'BFFENV'
DEV_KUBE_API_URL=http://localhost:8080/k8s
BASE_API_GROUP=dashboard.cozystack.io
BASE_API_VERSION=v1alpha1
BASE_NAVIGATION_RESOURCE_PLURAL=navigations
BASE_NAVIGATION_RESOURCE_NAME=navigation
BASE_FRONTEND_PREFIX=/openapi-ui
BASE_NAMESPACE_FULL_PATH=/apis/core.cozystack.io/v1alpha1/tenantnamespaces
BASE_FACTORY_NAMESPACED_API_KEY=base-factory-namespaced-api
BASE_FACTORY_CLUSTERSCOPED_API_KEY=base-factory-clusterscoped-api
BASE_FACTORY_NAMESPACED_BUILTIN_KEY=base-factory-namespaced-builtin
BASE_FACTORY_CLUSTERSCOPED_BUILTIN_KEY=base-factory-clusterscoped-builtin
BASE_NAMESPACE_FACTORY_KEY=base-factory-clusterscoped-builtin
BASE_ALLOWED_AUTH_HEADERS=user-agent,accept,content-type,application/json,origin,referer,accept-encoding,cookie
BFFENV
fi

# --- Start kubectl proxy + nginx reverse proxy ---
# nginx on :8080 mirrors the production self-proxy architecture:
#   /api/clusters/default/* → rewrite → self-proxy back to :8080
#   /k8s/*                  → kubectl proxy on :8081
#   /openapi-bff*           → BFF on :4002
#   /*                      → Vite dev server on :4001
log "Starting kubectl proxy on :8081..."
kubectl proxy --port=8081 &

log "Starting nginx reverse proxy on :8080..."
nginx -c "$SCRIPT_DIR/dev-nginx.conf" -p "$SCRIPT_DIR" &

# --- Start BFF ---
log "Starting BFF on :4002..."
(cd openapi-ui-k8s-bff && npm run dev) &

# --- Start UI ---
log "Starting UI on :4001..."
(cd openapi-ui && npm run dev) &

echo ""
echo -e "${GREEN}==========================================${NC}"
echo -e "${GREEN}  Cozystack Dashboard is running${NC}"
echo -e "${GREEN}  http://localhost:8080/openapi-ui${NC}"
echo -e "${GREEN}==========================================${NC}"
echo ""

wait
