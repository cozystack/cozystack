import { useLocation } from "react-router"

/**
 * Resource pages (list/detail/edit/order) are mounted under both /console and
 * /admin, so their internal navigation must stay within whichever portal the
 * user is browsing. Returns the active portal's base path; callers keep an
 * explicit "/console" only for deliberate "back to console home" fallbacks.
 */
export function useResourceBasePath(): "/console" | "/admin" {
  const { pathname } = useLocation()
  return pathname.startsWith("/admin") ? "/admin" : "/console"
}

/**
 * Where the app lands when it is opened without a path.
 *
 * The Console is the entry point: it shows what the user already runs, which is
 * what someone opening the dashboard almost always came for. The Marketplace is
 * a place you go deliberately, when you want to add something new — reachable
 * from its own tab, from the Console overview, and from the command palette.
 */
export const DEFAULT_LANDING_PATH = "/console"
