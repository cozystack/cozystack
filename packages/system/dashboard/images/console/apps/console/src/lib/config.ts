export interface AppConfig {
  titleText?: string
  footerText?: string
  logoText?: string
  logoSvg?: string
  iconSvg?: string
  // Platform version injected at deploy time by the chart (from the console
  // image tag), so a promoted-by-retag image reports the stable version rather
  // than the rc version baked into the bundle at build. Falls back to the
  // build-time VITE_APP_VERSION when absent.
  version?: string
}

const CONFIG_NAMESPACE = "cozy-dashboard"
const CONFIG_MAP_NAME = "cozy-dashboard-console-config"

// Branding served as a static asset by the console, mounted from the same
// ConfigMap. Read with no kube-api call at all, so branding renders even when
// the apiserver rejects the session's token (a dashboard login the kube-api
// does not authenticate) and without depending on the console-config-reader
// RBAC that the kube-api read otherwise needs.
const BRANDING_STATIC_PATH = "/branding/config.json"

// Static-first has two static-fetch outcomes and both are instant: the mounted
// file returns 200 JSON, or (chart without the mount) nginx serves index.html.
// The only slow path is a partially-stalled pod; a short budget then caps the
// static leg before the kube-api fallback, so worst case is this + the API's 5s
// rather than 5s + 5s. On the shipped chart the fallback never runs.
const BRANDING_STATIC_TIMEOUT_MS = 1500

function fetchWithTimeout(url: string, ms = 5000): Promise<Response> {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), ms)
  return fetch(url, { signal: ctrl.signal }).finally(() => clearTimeout(timer))
}

export async function loadConfig(): Promise<AppConfig> {
  const fromStatic = await loadStaticConfig()
  if (fromStatic) return fromStatic
  return loadConfigFromApi()
}

async function loadStaticConfig(): Promise<AppConfig | undefined> {
  try {
    const resp = await fetchWithTimeout(BRANDING_STATIC_PATH, BRANDING_STATIC_TIMEOUT_MS)
    if (!resp.ok) return undefined
    const cfg: unknown = await resp.json()
    // A chart without the branding mount serves the SPA index.html here; its
    // non-JSON body throws above, so only a real config object reaches this.
    return cfg && typeof cfg === "object" ? (cfg as AppConfig) : undefined
  } catch {
    return undefined
  }
}

async function loadConfigFromApi(): Promise<AppConfig> {
  try {
    const resp = await fetchWithTimeout(
      `/api/v1/namespaces/${CONFIG_NAMESPACE}/configmaps/${CONFIG_MAP_NAME}`,
    )
    if (!resp.ok) return {}
    const cm = await resp.json()
    const raw = cm?.data?.["config.json"]
    if (!raw) return {}
    return JSON.parse(raw) as AppConfig
  } catch {
    return {}
  }
}

export async function loadUsername(): Promise<string | undefined> {
  try {
    const resp = await fetchWithTimeout("/oauth2/userinfo")
    if (!resp.ok) return undefined
    const info = await resp.json() as { user?: string; email?: string }
    return info.email ?? info.user ?? undefined
  } catch {
    return undefined
  }
}
