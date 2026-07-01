import { useMemo } from "react"
import { Link } from "react-router"
import { Plus } from "lucide-react"
import { Spinner, Button } from "@cozystack/ui"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import {
  isTenantModule,
  useApplicationDefinitions,
  useApplicationInstances,
} from "../lib/app-definitions.ts"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { InstanceCard } from "../components/InstanceCard.tsx"
import { QuotaPanel } from "../components/QuotaDisplay.tsx"

function TenantApps({ ad, namespace }: { ad: ApplicationDefinition; namespace: string }) {
  const { data } = useApplicationInstances(ad, namespace)
  const items = data?.items ?? []
  if (items.length === 0) return null
  return (
    <section>
      <h2 className="mb-2 flex items-center gap-2 text-xs font-semibold uppercase tracking-wider text-slate-400">
        {ad.spec?.application.kind}
        <span className="rounded-full bg-slate-100 px-1.5 text-[10px] font-medium text-slate-600">
          {items.length}
        </span>
      </h2>
      <div className="flex flex-wrap gap-2">
        {items.map((inst: ApplicationInstance) => (
          <InstanceCard
            key={inst.metadata.name}
            ad={ad}
            instance={inst}
          />
        ))}
      </div>
    </section>
  )
}

export function ConsoleOverview() {
  const { data, isLoading } = useApplicationDefinitions()
  const { tenantNamespace, selectedTenant } = useTenantContext()

  const ads = useMemo(
    () =>
      (data?.items ?? [])
        .filter((ad) => !isTenantModule(ad) && ad.spec?.application.kind !== "Tenant")
        .slice()
        .sort((a, b) =>
          (a.spec?.application.kind ?? "").localeCompare(
            b.spec?.application.kind ?? "",
          ),
        ),
    [data],
  )

  if (!tenantNamespace) {
    return (
      <div className="p-6 text-sm text-slate-500">
        Select a tenant to view its deployed applications.
      </div>
    )
  }

  return (
    <div className="p-6">
      <div className="mb-5 flex items-end justify-between">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Deployed applications</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            Tenant <code className="text-slate-700">{selectedTenant}</code> ·{" "}
            <code className="text-slate-400">{tenantNamespace}</code>
          </p>
        </div>
        <Link to="/marketplace">
          <Button variant="primary" size="sm">
            <Plus className="size-3.5" /> Deploy application
          </Button>
        </Link>
      </div>
      <QuotaPanel namespace={tenantNamespace} />

      {isLoading ? (
        <div className="mt-6 flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : (
        <div className="mt-6 space-y-6">
          {ads.map((ad) => (
            <TenantApps key={ad.metadata.name} ad={ad} namespace={tenantNamespace} />
          ))}
        </div>
      )}
    </div>
  )
}
