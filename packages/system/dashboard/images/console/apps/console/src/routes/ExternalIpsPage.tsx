import { useCallback, useEffect, useMemo, useState } from "react"
import { Globe } from "lucide-react"
import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import { Section, Spinner } from "@cozystack/ui"
import type { TenantNamespace } from "@cozystack/types"
import { formatAge } from "../lib/status.ts"

interface ServiceSpec {
  type?: string
  ports?: { port: number; protocol?: string; name?: string; nodePort?: number }[]
  clusterIP?: string
}

interface ServiceStatus {
  loadBalancer?: {
    ingress?: { ip?: string; hostname?: string }[]
  }
}

/**
 * Administration → External IPs. One table of `LoadBalancer` services across
 * every tenant namespace the user can see (all visible TenantNamespaces, not
 * just the selected subtree). Namespaces without such services — or where the
 * service list is not permitted — contribute no rows.
 */
export function ExternalIpsPage() {
  const { data: tnList, isLoading: tnLoading } = useK8sList<TenantNamespace>({
    apiGroup: "core.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "tenantnamespaces",
  })

  const tenants = useMemo(
    () =>
      (tnList?.items ?? [])
        .slice()
        .sort((a, b) => a.metadata.name.localeCompare(b.metadata.name)),
    [tnList],
  )

  // Every namespace reports its LoadBalancer count once settled, so the page
  // can tell "still loading" apart from "nothing anywhere".
  const [counts, setCounts] = useState<Record<string, number>>({})
  const onCount = useCallback((ns: string, n: number) => {
    setCounts((prev) => (prev[ns] === n ? prev : { ...prev, [ns]: n }))
  }, [])

  const allSettled =
    !tnLoading && tenants.every((t) => counts[t.metadata.name] !== undefined)
  const total = tenants.reduce((s, t) => s + (counts[t.metadata.name] ?? 0), 0)

  return (
    <div className="p-6">
      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-10 items-center justify-center rounded-md bg-slate-100 text-slate-500">
          <Globe className="size-5" />
        </div>
        <div>
          <h1 className="text-xl font-semibold text-slate-900">External IPs</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            LoadBalancer services across every tenant you can access.
          </p>
        </div>
      </div>

      {/* Row providers stay mounted (their watches feed the counts), so hide
          the empty table with CSS instead of unmounting it. */}
      <div className={total === 0 ? "hidden" : undefined}>
        <Section bodyClassName="p-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 bg-slate-50 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                <th className="px-4 py-3">Namespace</th>
                <th className="px-4 py-3">Service</th>
                <th className="px-4 py-3">External</th>
                <th className="px-4 py-3">Ports</th>
                <th className="px-4 py-3">Age</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {tenants.map((tn) => (
                <TenantIpsRows key={tn.metadata.name} tn={tn} onCount={onCount} />
              ))}
            </tbody>
          </table>
        </Section>
      </div>

      {!allSettled ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : total === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">
            No LoadBalancer services in any accessible tenant.
          </p>
        </Section>
      ) : null}
    </div>
  )
}

function TenantIpsRows({
  tn,
  onCount,
}: {
  tn: TenantNamespace
  onCount: (ns: string, n: number) => void
}) {
  const ns = tn.metadata.name
  const { data, isLoading, error } = useK8sList<K8sResource<ServiceSpec, ServiceStatus>>(
    { apiGroup: "", apiVersion: "v1", plural: "services", namespace: ns },
    { fieldSelector: "spec.type=LoadBalancer" },
  )
  const items = data?.items ?? []

  useEffect(() => {
    // A forbidden namespace settles as empty rather than blocking the page.
    if (!isLoading) onCount(ns, error ? 0 : items.length)
  }, [ns, isLoading, error, items.length, onCount])

  if (isLoading || error) return null

  return (
    <>
      {items.map((svc) => {
        const lb = svc.status?.loadBalancer?.ingress?.[0]
        const external = lb?.ip ?? lb?.hostname ?? "Pending"
        return (
          <tr key={`${ns}/${svc.metadata.name}`}>
            <td className="px-4 py-3 font-mono text-xs text-slate-600">{ns}</td>
            <td className="px-4 py-3 font-mono text-xs text-slate-800">
              {svc.metadata.name}
            </td>
            <td className="px-4 py-3 font-mono text-xs text-slate-700">
              {external}
            </td>
            <td className="px-4 py-3 text-slate-600">
              {svc.spec?.ports
                ?.map((p) => `${p.port}/${p.protocol ?? "TCP"}`)
                .join(", ") ?? "—"}
            </td>
            <td className="px-4 py-3 tabular-nums text-xs text-slate-500">
              {formatAge(svc.metadata.creationTimestamp)}
            </td>
          </tr>
        )
      })}
    </>
  )
}
