import { useState } from "react"

/**
 * Whether the browser runs on a Mac, so callers can render the right modifier
 * key. Read once on first render — the platform cannot change while the SPA
 * is open, and this app never renders on a server.
 */
export function useIsMac(): boolean {
  const [isMac] = useState(() => navigator.platform.toUpperCase().indexOf("MAC") >= 0)

  return isMac
}
