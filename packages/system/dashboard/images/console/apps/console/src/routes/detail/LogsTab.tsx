import type { ApplicationDefinition } from "@cozystack/types"
import { useTenantContext } from "../../lib/tenant-context.tsx"
import { LogsView } from "../../components/observability/LogsView.tsx"

interface LogsTabProps {
  ad: ApplicationDefinition
  // The route's name: while the next application loads, the page still holds the previous instance.
  name: string
}

export function LogsTab({ ad, name }: LogsTabProps) {
  const { tenantNamespace } = useTenantContext()
  const plural = ad.spec?.application.plural
  if (!tenantNamespace || !plural) return null
  return <LogsView scope={{ namespace: tenantNamespace, plural, name }} />
}
