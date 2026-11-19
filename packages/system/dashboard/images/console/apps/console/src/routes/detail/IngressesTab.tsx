import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import { Section, Spinner } from "@cozystack/ui"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { appInstanceLabel } from "../../lib/labels.ts"

interface IngressSpec {
  rules?: {
    host?: string
    http?: { paths?: { path?: string; backend?: { service?: { name: string } } }[] }
  }[]
  tls?: { hosts?: string[] }[]
}

export function IngressesTab({
  ad,
  instance,
}: {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}) {
  const ns = instance.metadata.namespace ?? ""
  const { data, isLoading } = useK8sList<K8sResource<IngressSpec>>(
    { apiGroup: "networking.k8s.io", apiVersion: "v1", plural: "ingresses", namespace: ns },
    { labelSelector: appInstanceLabel(ad, instance) },
  )
  const items = data?.items ?? []

  return (
    <div className="p-6">
      <Section title="Ingresses" bodyClassName="p-0">
        {isLoading ? (
          <div className="flex items-center gap-2 px-5 py-4 text-xs text-slate-500">
            <Spinner /> Loading…
          </div>
        ) : items.length === 0 ? (
          <div className="px-5 py-6 text-sm text-slate-500">No ingresses.</div>
        ) : (
          <ul className="divide-y divide-slate-100">
            {items.map((ing) => (
              <li key={ing.metadata.name} className="px-5 py-3">
                <p className="font-mono text-xs text-slate-800">
                  {ing.metadata.name}
                </p>
                <ul className="mt-1.5 space-y-1 text-xs text-slate-600">
                  {ing.spec?.rules?.map((rule, idx) => (
                    <li key={idx} className="flex flex-wrap gap-x-2">
                      {rule.host && (
                        <a
                          href={`https://${rule.host}`}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="font-medium text-blue-700 hover:underline"
                        >
                          {rule.host}
                        </a>
                      )}
                      {rule.http?.paths?.map((p, i) => (
                        <span key={i} className="text-slate-500">
                          {p.path ?? "/"} → {p.backend?.service?.name}
                        </span>
                      ))}
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </div>
  )
}
