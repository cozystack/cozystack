import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { useTenantContext } from "../../lib/tenant-context.tsx"
import { MetricsView } from "../../components/observability/MetricsView.tsx"

interface MetricsTabProps {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}

export function MetricsTab({ ad, instance }: MetricsTabProps) {
  const { tenantNamespace } = useTenantContext()
  const plural = ad.spec?.application.plural
  if (!tenantNamespace || !plural) return null
  return <MetricsView scope={{ namespace: tenantNamespace, plural, name: instance.metadata.name }} />
}
