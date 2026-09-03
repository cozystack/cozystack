import { Navigate } from "react-router"
import { useApplicationDefinitions } from "../lib/app-definitions.ts"
import { useResourceBasePath } from "../lib/portal.ts"

/**
 * `/console/info` (and `/admin/info`) is a convenience path for the Info
 * singleton. We resolve the AD lazily and redirect to the generic detail route
 * for whatever plural it declares (typically `infos`), staying in the active
 * portal so /admin/info lands under /admin.
 */
export function InfoRedirect() {
  const { data, isLoading } = useApplicationDefinitions()
  const base = useResourceBasePath()
  if (isLoading) return null
  const ad = data?.items.find((d) => d.spec?.application.kind === "Info")
  // Stay in the active portal on the miss too — hardcoding /console here is
  // what the docstring above promises not to do, and it throws an admin out of
  // /admin whenever the Info definition is absent or its query errored.
  if (!ad) return <Navigate to={base} replace />
  const plural = ad.spec?.application.plural ?? "infos"
  return <Navigate to={`${base}/${plural}/info`} replace />
}
