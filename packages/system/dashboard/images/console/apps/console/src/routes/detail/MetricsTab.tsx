import type { ApplicationDefinition } from "@cozystack/types"
import { useTenantContext } from "../../lib/tenant-context.tsx"
import { MetricsView } from "../../components/observability/MetricsView.tsx"

interface MetricsTabProps {
  ad: ApplicationDefinition
  // The route's name: while the next application loads, the page still holds the previous instance.
  name: string
}

export function MetricsTab({ ad, name }: MetricsTabProps) {
  const { tenantNamespace } = useTenantContext()
  const plural = ad.spec?.application.plural
  if (!tenantNamespace || !plural) return null
  return <MetricsView scope={{ namespace: tenantNamespace, plural, name }} />
}
