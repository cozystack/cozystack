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
