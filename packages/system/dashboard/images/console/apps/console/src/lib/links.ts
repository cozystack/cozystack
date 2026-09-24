import type { MouseEvent } from "react"

/**
 * Whether a click on an anchor will navigate the tab it happened in, rather
 * than open a new tab or window. React runs `onClick` either way, and a
 * cross-tenant link's handler aligns the tenant context of the tab it is
 * leaving — which is wrong when that tab is staying where it is. Same button
 * and modifier test react-router's own `Link` applies before it intercepts a
 * click, minus the `target` check these links have no attribute for.
 */
export function navigatesThisTab(e: MouseEvent): boolean {
  return e.button === 0 && !e.metaKey && !e.altKey && !e.ctrlKey && !e.shiftKey
}
