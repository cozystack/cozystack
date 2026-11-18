import { useTenantContext } from "../lib/tenant-context.tsx"
import { LogsView } from "../components/observability/LogsView.tsx"

export function LogsPage() {
  const { tenantNamespace } = useTenantContext()
  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-slate-200 bg-white px-6 py-4">
        <h1 className="text-lg font-semibold text-slate-900">Logs</h1>
        <p className="text-xs text-slate-500">Every application in {tenantNamespace ?? "this tenant"}</p>
      </div>
      {tenantNamespace && <LogsView scope={{ namespace: tenantNamespace }} />}
    </div>
  )
}
