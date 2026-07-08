import { useMemo } from "react"
import { useParams } from "react-router"
import { Spinner } from "@cozystack/ui"
import { useK8sGet } from "@cozystack/k8s-client"
import {
  APPS_GROUP,
  APPS_VERSION,
  type ApplicationInstance,
} from "@cozystack/types"
import { useApplicationDefinitions } from "../../lib/app-definitions.ts"
import { useTenantContext } from "../../lib/tenant-context.tsx"
import { ApplicationOrderPage } from "../ApplicationOrderPage.tsx"

/**
 * Fetches the existing application instance and hands it to ApplicationOrder
 * Page in edit mode, so the same form serves both create and update flows.
 */
export function ApplicationEditRoute() {
  const { plural, name } = useParams<{ plural: string; name: string }>()
  const { data: defs } = useApplicationDefinitions()
  const { tenantNamespace } = useTenantContext()

  const ad = useMemo(
    () => defs?.items.find((d) => d.spec?.application.plural === plural),
    [defs, plural],
  )

  const { data: instance, isLoading } = useK8sGet<ApplicationInstance>(
    {
      apiGroup: APPS_GROUP,
      apiVersion: APPS_VERSION,
      plural: plural ?? "",
      name: name ?? "",
      namespace: tenantNamespace ?? "",
    },
    { enabled: !!plural && !!name && !!tenantNamespace },
  )

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 p-8 text-slate-500">
        <Spinner /> Loading…
      </div>
    )
  }
  if (!ad || !instance) {
    return <div className="p-8 text-red-600">Not found.</div>
  }

  return (
    <ApplicationOrderPage
      appNameOverride={ad.metadata.name}
      editMode={{ name: instance.metadata.name, initialSpec: instance.spec ?? {} }}
    />
  )
}
