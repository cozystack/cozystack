import { Navigate } from "react-router"
import { useApplicationDefinitions } from "../lib/app-definitions.ts"

/**
 * `/console/info` is a convenience path for the Info singleton. We resolve
 * the AD lazily and redirect to the generic detail route for whatever plural
 * it declares (typically `infos`).
 */
export function InfoRedirect() {
  const { data, isLoading } = useApplicationDefinitions()
  if (isLoading) return null
  const ad = data?.items.find((d) => d.spec?.application.kind === "Info")
  if (!ad) return <Navigate to="/console" replace />
  const plural = ad.spec?.application.plural ?? "infos"
  return <Navigate to={`/console/${plural}/info`} replace />
}
