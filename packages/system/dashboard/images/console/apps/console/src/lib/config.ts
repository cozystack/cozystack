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

function fetchWithTimeout(url: string, ms = 5000): Promise<Response> {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), ms)
  return fetch(url, { signal: ctrl.signal }).finally(() => clearTimeout(timer))
}

export async function loadConfig(): Promise<AppConfig> {
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
