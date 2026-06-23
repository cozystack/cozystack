import { Globe } from "lucide-react"
import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import { Section, Spinner } from "@cozystack/ui"
import { useTenantContext } from "../lib/tenant-context.tsx"
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
 * Administration → External IPs. Lists every `Service` in the current tenant
 * namespace whose `spec.type` is `LoadBalancer`, showing the assigned
 * external IP (or hostname) and the exposed ports.
 */
export function ExternalIpsPage() {
  const { tenantNamespace } = useTenantContext()
  const { data, isLoading } = useK8sList<K8sResource<ServiceSpec, ServiceStatus>>(
    {
      apiGroup: "",
      apiVersion: "v1",
      plural: "services",
      namespace: tenantNamespace ?? undefined,
    },
    { fieldSelector: "spec.type=LoadBalancer", enabled: !!tenantNamespace },
  )

  const items = data?.items ?? []

  return (
    <div className="p-6">
      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-10 items-center justify-center rounded-md bg-slate-100 text-slate-500">
          <Globe className="size-5" />
        </div>
        <div>
          <h1 className="text-xl font-semibold text-slate-900">External IPs</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            LoadBalancer services exposing traffic outside the cluster.
          </p>
        </div>
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : items.length === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">
            No LoadBalancer services in this tenant.
          </p>
        </Section>
      ) : (
        <Section bodyClassName="p-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 bg-slate-50 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                <th className="px-4 py-3">Service</th>
                <th className="px-4 py-3">External</th>
                <th className="px-4 py-3">Ports</th>
                <th className="px-4 py-3">Age</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((svc) => {
                const lb = svc.status?.loadBalancer?.ingress?.[0]
                const external = lb?.ip ?? lb?.hostname ?? "Pending"
                return (
                  <tr key={`${svc.metadata.namespace}/${svc.metadata.name}`}>
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
            </tbody>
          </table>
        </Section>
      )}
    </div>
  )
}
