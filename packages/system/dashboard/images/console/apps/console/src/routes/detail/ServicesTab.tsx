import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import { Section, Spinner } from "@cozystack/ui"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { appInstanceLabel } from "../../lib/labels.ts"
import { formatAge } from "../../lib/status.ts"

interface ServiceSpec {
  type?: string
  clusterIP?: string
  ports?: { port: number; protocol?: string; name?: string }[]
}

export function ServicesTab({
  ad,
  instance,
}: {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}) {
  const ns = instance.metadata.namespace ?? ""
  const { data, isLoading } = useK8sList<K8sResource<ServiceSpec>>(
    { apiGroup: "", apiVersion: "v1", plural: "services", namespace: ns },
    { labelSelector: appInstanceLabel(ad, instance) },
  )
  const items = data?.items ?? []

  return (
    <div className="p-6">
      <Section title="Services" bodyClassName="p-0">
        {isLoading ? (
          <div className="flex items-center gap-2 px-5 py-4 text-xs text-slate-500">
            <Spinner /> Loading…
          </div>
        ) : items.length === 0 ? (
          <div className="px-5 py-6 text-sm text-slate-500">No services.</div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 bg-slate-50 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                <th className="px-4 py-3">Name</th>
                <th className="px-4 py-3">Type</th>
                <th className="px-4 py-3">Cluster IP</th>
                <th className="px-4 py-3">Ports</th>
                <th className="px-4 py-3">Age</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((svc) => (
                <tr key={svc.metadata.name} className="hover:bg-slate-50">
                  <td className="px-4 py-3 font-mono text-xs text-slate-800">
                    {svc.metadata.name}
                  </td>
                  <td className="px-4 py-3 text-slate-700">
                    {svc.spec?.type ?? "ClusterIP"}
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-slate-600">
                    {svc.spec?.clusterIP ?? "—"}
                  </td>
                  <td className="px-4 py-3 text-slate-700">
                    {svc.spec?.ports
                      ?.map((p) => `${p.port}/${p.protocol ?? "TCP"}`)
                      .join(", ") ?? "—"}
                  </td>
                  <td className="px-4 py-3 tabular-nums text-slate-500">
                    {formatAge(svc.metadata.creationTimestamp)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </div>
  )
}
