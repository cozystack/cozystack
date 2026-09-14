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
// ConfigMap. Read without the kube-api, so the access-denied screen renders
// branded for a signed-in user who has no cluster RBAC.
const BRANDING_STATIC_PATH = "/branding/config.json"

// The static asset is a local file behind the same nginx, so it should answer
// immediately; a short budget keeps first paint from stalling on it before the
// kube-api fallback, which keeps the default 5s.
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
