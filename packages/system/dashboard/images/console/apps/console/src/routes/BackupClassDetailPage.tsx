import { Link, useNavigate, useParams } from "react-router"
import yaml from "js-yaml"
import { ArrowLeft, Archive, Edit, Trash2 } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sDelete, useK8sGet, type K8sResource } from "@cozystack/k8s-client"
import { formatAge } from "../lib/status.ts"

export function BackupClassDetailPage() {
  const { name } = useParams<{ name: string }>()
  const navigate = useNavigate()

  // BackupClass is cluster-scoped — not tenant-namespaced.
  const { data: backupClass, isLoading, error } = useK8sGet<K8sResource>(
    {
      apiGroup: "backups.cozystack.io",
      apiVersion: "v1alpha1",
      plural: "backupclasses",
      name: name ?? "",
    },
    { enabled: !!name },
  )

  const deleteMutation = useK8sDelete({
    apiGroup: "backups.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "backupclasses",
  })

  const handleDelete = async () => {
    if (!name) return
    if (!confirm(`Delete Backup Class "${name}"? This cannot be undone.`)) return
    try {
      await deleteMutation.mutateAsync(name)
      navigate("/admin/backups/backupclasses")
    } catch (err) {
      alert(`Failed to delete Backup Class: ${(err as Error).message}`)
    }
  }

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 p-6 text-sm text-slate-500">
        <Spinner /> Loading…
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-6 text-sm text-red-600">
        Failed to load Backup Class: {(error as Error).message}
      </div>
    )
  }

  if (!backupClass) {
    return <div className="p-6 text-sm text-red-600">Backup Class not found.</div>
  }

  const labelEntries = Object.entries(backupClass.metadata.labels ?? {})

  return (
    <div className="p-6">
      <Link
        to="/admin/backups/backupclasses"
        className="mb-4 inline-flex items-center gap-1 text-xs text-slate-500 hover:text-slate-700"
      >
        <ArrowLeft className="size-3.5" /> Backups
      </Link>

      <div className="mb-5 flex items-end justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
            <Archive className="size-6 text-slate-600" />
          </div>
          <div>
            <h1 className="text-lg font-semibold text-slate-900">
              {backupClass.metadata.name}
            </h1>
            <p className="text-xs text-slate-500">Backup Class</p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Link to={`/admin/backups/backupclasses/${backupClass.metadata.name}/edit`}>
            <Button variant="primary" size="sm">
              <Edit className="size-3.5" /> Edit
            </Button>
          </Link>
          <Button
            variant="destructive"
            size="sm"
            onClick={handleDelete}
            disabled={deleteMutation.isPending}
          >
            <Trash2 className="size-3.5" /> Delete
          </Button>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <Section title="Details" className="md:col-span-1">
          <dl className="space-y-2.5 text-sm">
            <div className="flex items-center justify-between">
              <dt className="text-slate-500">Name</dt>
              <dd className="font-mono text-xs text-slate-700">
                {backupClass.metadata.name}
              </dd>
            </div>
            <div className="flex items-center justify-between">
              <dt className="text-slate-500">Age</dt>
              <dd className="tabular-nums text-slate-700">
                {formatAge(backupClass.metadata.creationTimestamp)}
              </dd>
            </div>
          </dl>
          {labelEntries.length > 0 && (
            <div className="mt-3 border-t border-slate-100 pt-3">
              <p className="mb-1.5 text-xs text-slate-500">Labels</p>
              <div className="flex flex-wrap gap-1.5">
                {labelEntries.map(([k, v]) => (
                  <span
                    key={k}
                    className="inline-flex rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-700"
                  >
                    {k}={v}
                  </span>
                ))}
              </div>
            </div>
          )}
        </Section>

        <Section title="Spec" className="md:col-span-2" bodyClassName="p-0">
          <pre className="max-h-[60vh] overflow-auto p-5 text-xs leading-relaxed text-slate-800">
            {yaml.dump(backupClass.spec ?? {})}
          </pre>
        </Section>
      </div>
    </div>
  )
}
