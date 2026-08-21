import { useState } from "react"
import { Package, ShieldAlert } from "lucide-react"
import { Spinner, StatusBadge } from "@cozystack/ui"
import { K8sApiError } from "@cozystack/k8s-client"
import { CORE_GROUP, CORE_VERSION, type Tap } from "@cozystack/types"
import {
  useTaps,
  useConnectTap,
  useDisconnectTap,
  deriveTapName,
} from "../lib/taps.ts"

export function TapsPage() {
  const { data, isLoading, error } = useTaps()
  const connect = useConnectTap()
  const disconnect = useDisconnectTap()

  const [url, setUrl] = useState("")
  const [secret, setSecret] = useState("")

  const onConnect = async () => {
    const trimmed = url.trim()
    if (!trimmed) return
    const body: Tap = {
      apiVersion: `${CORE_GROUP}/${CORE_VERSION}`,
      kind: "Tap",
      metadata: { name: deriveTapName(trimmed) },
      spec: { url: trimmed, secretRef: secret.trim() || undefined },
    }
    try {
      await connect.mutateAsync(body)
      setUrl("")
      setSecret("")
    } catch (err) {
      const message = err instanceof K8sApiError ? err.message : String(err)
      alert(`Failed to connect repository: ${message}`)
    }
  }

  const onDisconnect = async (name: string) => {
    if (!confirm(`Disconnect repository "${name}"? Installed packages stay installed.`)) return
    try {
      await disconnect.mutateAsync(name)
    } catch (err) {
      const message = err instanceof K8sApiError ? err.message : String(err)
      alert(`Failed to disconnect: ${message}`)
    }
  }

  const taps = data?.items ?? []

  return (
    <div className="p-6">
      <div className="mb-5">
        <h1 className="text-xl font-semibold text-slate-900">Repositories</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          External-Apps repositories connected to this cluster and the packages they expose.
        </p>
      </div>

      <div className="mb-6 flex flex-wrap items-end gap-2 rounded-lg border border-slate-200 bg-white p-4">
        <label className="flex flex-col text-xs font-medium text-slate-500">
          Repository (oci:// reference)
          <input
            className="mt-1 w-96 rounded-md border border-slate-300 px-2 py-1.5 text-sm text-slate-900"
            placeholder="oci://ghcr.io/org/repo:tag"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
          />
        </label>
        <label className="flex flex-col text-xs font-medium text-slate-500">
          Pull secret (optional, for private repositories)
          <input
            className="mt-1 w-64 rounded-md border border-slate-300 px-2 py-1.5 text-sm text-slate-900"
            placeholder="cozy-system Secret name"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
          />
        </label>
        <button
          type="button"
          className="inline-flex items-center rounded-md bg-slate-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-50"
          disabled={!url.trim() || connect.isPending}
          onClick={onConnect}
        >
          Connect
        </button>
      </div>

      {isLoading && (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading repositories…
        </div>
      )}
      {error && (
        <div className="text-sm text-red-600">
          Failed to load repositories: {(error as Error).message}
        </div>
      )}

      {!isLoading && !error && taps.length === 0 && (
        <div className="rounded-lg border border-dashed border-slate-300 bg-white p-12 text-center text-sm text-slate-500">
          No repositories connected yet. Connect one above, or with{" "}
          <code>cozypkg tap</code>.
        </div>
      )}

      <div className="space-y-3">
        {taps.map((tap) => (
          <TapCard key={tap.metadata.name} tap={tap} onDisconnect={onDisconnect} />
        ))}
      </div>
    </div>
  )
}

function TapCard({ tap, onDisconnect }: { tap: Tap; onDisconnect: (name: string) => void }) {
  const name = tap.metadata.name
  const spec = tap.spec ?? {}
  const packages = spec.packages ?? []
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-semibold text-slate-900">{name}</h3>
            <span className="rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-500">
              {spec.community ? "community" : "official"}
            </span>
            <StatusBadge tone={spec.ready ? "ok" : "warn"}>
              {spec.ready ? "Ready" : "Pending"}
            </StatusBadge>
          </div>
          {spec.source?.kind && (
            <p className="mt-0.5 text-xs text-slate-500">
              {spec.source.kind} {spec.source.name}
            </p>
          )}
          {!spec.ready && spec.message && (
            <p className="mt-0.5 text-xs text-amber-600">{spec.message}</p>
          )}
        </div>
        {spec.community && (
          <button
            type="button"
            className="inline-flex items-center rounded-md border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50"
            onClick={() => onDisconnect(name)}
          >
            Disconnect
          </button>
        )}
      </div>

      {packages.length > 0 && (
        <ul className="mt-3 flex flex-wrap gap-2">
          {packages.map((pkg) => (
            <li
              key={pkg.name}
              className="flex items-center gap-1 rounded-md border border-slate-200 px-2 py-1 text-xs text-slate-700"
              title={pkg.description}
            >
              <Package className="size-3 text-slate-400" />
              {pkg.kind || pkg.name}
              {pkg.privileged && (
                <span className="flex items-center gap-0.5 text-amber-600">
                  <ShieldAlert className="size-3" /> privileged
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
