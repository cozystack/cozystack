import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { useTenantContext } from "../../lib/tenant-context.tsx"
import { LogsView } from "../../components/observability/LogsView.tsx"

interface LogsTabProps {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}

export function LogsTab({ ad, instance }: LogsTabProps) {
  const { tenantNamespace } = useTenantContext()
  const plural = ad.spec?.application.plural
  if (!tenantNamespace || !plural) return null
  return <LogsView scope={{ namespace: tenantNamespace, plural, name: instance.metadata.name }} />
}
