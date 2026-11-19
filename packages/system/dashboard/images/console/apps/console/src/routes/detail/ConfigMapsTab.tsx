import { useState } from "react"
import { useK8sList } from "@cozystack/k8s-client"
import { Button, Section, Spinner } from "@cozystack/ui"
import type { ConfigMap } from "./use-app-configmaps.ts"

function ConfigValue({ name, value }: { name: string, value: string }) {
  const [copyResult, setCopyResult] = useState<{ value: string, copied: boolean } | null>(null)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopyResult({ value, copied: true })
    } catch {
      setCopyResult({ value, copied: false })
    }
  }
  return (
    <div className="space-y-2 p-4">
      <div className="flex items-center justify-between gap-3">
        <code className="break-all text-xs">{name}</code>
        <Button size="sm" variant="outline" disabled={!navigator.clipboard} onClick={copy}>
          Copy {name}
        </Button>
      </div>
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-slate-900 p-3 text-xs text-slate-100">{value || "(empty)"}</pre>
      {copyResult?.value === value && <p role="status" className="text-xs">{copyResult.copied ? "Copied." : "Could not copy. Select and copy the value manually."}</p>}
    </div>
  )
}

function ConfigMapItem({ namespace, name }: { namespace: string, name: string }) {
  const { data, isLoading, error } = useK8sList<ConfigMap>(
    { apiGroup: "", apiVersion: "v1", plural: "configmaps", namespace },
    { enabled: !!namespace, fieldSelector: `metadata.name=${name}`, retry: false },
  )
  const item = data?.items.find((entry) => entry.metadata.name === name)
  return (
    <Section title={name} bodyClassName="p-0">
      {error ? <p role="alert" className="p-4 text-red-600">{error.message}</p>
        : isLoading ? <div className="flex items-center gap-2 p-4"><Spinner /> Loading…</div>
        : !item ? <p className="p-4">ConfigMap is not available yet.</p>
        : Object.keys(item.data ?? {}).length === 0 ? <p className="p-4">No configuration values.</p>
        : <div className="divide-y divide-slate-100">{Object.entries(item.data ?? {}).map(([key, value]) => (
          <ConfigValue key={key} name={key} value={value} />
        ))}</div>}
    </Section>
  )
}

export function ConfigMapsTab({ namespace, names, error }: { namespace: string, names: string[], error: Error | null }) {
  return (
    <div className="space-y-6 p-6">
      {error ? <p role="alert" className="text-red-600">{error.message}</p>
        : names.length === 0 ? <p>No ConfigMaps.</p>
        : names.map((name) => <ConfigMapItem key={`${namespace}/${name}`} namespace={namespace} name={name} />)}
    </div>
  )
}
