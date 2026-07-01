import { useMemo } from "react"
import { Link, useNavigate, useParams } from "react-router"
import { Plus } from "lucide-react"
import { Button, Section, Spinner, StatusBadge } from "@cozystack/ui"
import type { ApplicationInstance } from "@cozystack/types"
import { useApplicationDefinitions, useApplicationInstances, iconDataUrl, appDisplayName } from "../lib/app-definitions.ts"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { formatAge, readyCondition } from "../lib/status.ts"
import { humanizeKind } from "../lib/humanize.ts"

/**
 * Generic instances list for a given AD plural. Reads the AD, pulls its
 * instances from the selected tenant namespace and renders a table.
 */
export function ApplicationListPage() {
  const { plural } = useParams<{ plural: string }>()
  const navigate = useNavigate()
  const { tenantNamespace, selectedTenant } = useTenantContext()
  const { data: defs, isLoading: defsLoading } = useApplicationDefinitions()

  const ad = useMemo(
    () => defs?.items.find((d) => d.spec?.application.plural === plural),
    [defs, plural],
  )

  const { data, isLoading } = useApplicationInstances(ad, tenantNamespace ?? undefined)
  const items = data?.items ?? []

  if (defsLoading) {
    return (
      <div className="flex items-center gap-2 p-6 text-sm text-slate-500">
        <Spinner /> Loading…
      </div>
    )
  }
  // defs have loaded; an unresolved plural is genuinely unknown, not pending.
  if (!ad) {
    return <div className="p-6 text-sm text-red-600">Unknown application type.</div>
  }

  const kind = ad.spec?.application.kind ?? ad.metadata.name
  const icon = iconDataUrl(ad)
  const pluralLabel = humanizeKind(kind)

  return (
    <div className="p-6">
      <div className="mb-5 flex items-end justify-between gap-4">
        <div className="flex items-center gap-3">
          {icon && (
            <div className="size-11 shrink-0 overflow-hidden rounded-md bg-slate-100">
              <img src={icon} alt="" className="h-full w-full" />
            </div>
          )}
          <div>
            <h1 className="text-xl font-semibold text-slate-900">{pluralLabel}</h1>
            <p className="mt-0.5 text-sm text-slate-500">
              {ad.spec?.dashboard?.description ?? `Manage ${appDisplayName(ad)} instances`}
              {selectedTenant && (
                <>
                  {" "}· Tenant <code className="text-slate-700">{selectedTenant}</code>
                </>
              )}
            </p>
          </div>
        </div>
        <Link to={`/console/new/${ad.metadata.name}`}>
          <Button variant="primary" size="sm">
            <Plus className="size-3.5" /> Deploy {humanizeKind(kind)}
          </Button>
        </Link>
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : items.length === 0 ? (
        <Section>
          <div className="py-8 text-center">
            <p className="text-sm text-slate-500">No {pluralLabel.toLowerCase()} yet.</p>
            <Link to={`/console/new/${ad.metadata.name}`} className="mt-3 inline-flex">
              <Button variant="primary" size="sm">
                <Plus className="size-3.5" /> Deploy the first one
              </Button>
            </Link>
          </div>
        </Section>
      ) : (
        <Section bodyClassName="p-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 bg-slate-50 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                <th className="px-4 py-3">Name</th>
                <th className="px-4 py-3">Status</th>
                <th className="px-4 py-3">Version</th>
                <th className="px-4 py-3">Age</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((inst: ApplicationInstance) => {
                const ready = readyCondition(inst)
                return (
                  <tr
                    key={inst.metadata.name}
                    onClick={() => navigate(`/console/${plural}/${inst.metadata.name}`)}
                    className="cursor-pointer transition-colors duration-100 hover:bg-slate-50"
                  >
                    <td className="px-4 py-3 font-mono text-xs text-slate-800">
                      {inst.metadata.name}
                    </td>
                    <td className="px-4 py-3">
                      {inst.metadata.deletionTimestamp ? (
                        <StatusBadge tone="muted">Terminating</StatusBadge>
                      ) : ready ? (
                        <StatusBadge tone={ready.status === "True" ? "ok" : "warn"}>
                          {ready.status === "True" ? "Ready" : (ready.reason ?? "NotReady")}
                        </StatusBadge>
                      ) : (
                        <StatusBadge tone="muted">Unknown</StatusBadge>
                      )}
                    </td>
                    <td className="px-4 py-3 font-mono text-xs text-slate-600">
                      {inst.status?.version ?? "—"}
                    </td>
                    <td className="px-4 py-3 tabular-nums text-xs text-slate-500">
                      {formatAge(inst.metadata.creationTimestamp)}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </Section>
      )}
    </div>
  )
}
