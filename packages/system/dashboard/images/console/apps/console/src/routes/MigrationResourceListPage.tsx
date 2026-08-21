import { Link } from "react-router"
import { Plus, Trash2, Import, Plug } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sList, useK8sDelete } from "@cozystack/k8s-client"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { formatAge } from "../lib/status.ts"
import {
  MIGRATION_GROUP,
  MIGRATION_VERSION,
  type MigrationResource,
  type MigrationResourceType,
  readyCondition,
  taskProgress,
} from "../lib/migration.ts"

interface MigrationResourceListPageProps {
  resourceType: MigrationResourceType
  title: string
}

export function MigrationResourceListPage({ resourceType, title }: MigrationResourceListPageProps) {
  const { tenantNamespace } = useTenantContext()
  const isTask = resourceType === "vmimporttasks"

  const { data, isLoading, error, refetch } = useK8sList<MigrationResource>({
    apiGroup: MIGRATION_GROUP,
    apiVersion: MIGRATION_VERSION,
    plural: resourceType,
    namespace: tenantNamespace ?? "",
  }, { enabled: !!tenantNamespace })

  const deleteMutation = useK8sDelete({
    apiGroup: MIGRATION_GROUP,
    apiVersion: MIGRATION_VERSION,
    plural: resourceType,
    namespace: tenantNamespace ?? "",
  })

  const items = data?.items ?? []

  const handleDelete = async (name: string) => {
    // Spelling out that the imported machines survive matters here: deleting a
    // finished import is the normal way to tidy up, and a tenant who fears it
    // will delete the VMs will simply never clean anything up.
    const warning = isTask
      ? `Delete import task "${name}"? Imported disks and instances are not affected.`
      : `Delete connection "${name}"? Imports already completed are not affected.`
    if (!confirm(warning)) return

    try {
      await deleteMutation.mutateAsync(name)
      refetch()
    } catch (err) {
      alert(`Failed to delete: ${(err as Error).message}`)
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
        Failed to load {title}: {(error as Error).message}
      </div>
    )
  }

  const Icon = isTask ? Import : Plug

  return (
    <div className="p-6">
      <div className="mb-5 flex items-end justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
            <Icon className="size-6 text-slate-600" />
          </div>
          <div>
            <h1 className="text-lg font-semibold text-slate-900">{title}</h1>
            <p className="text-xs text-slate-500">
              {items.length} {items.length === 1 ? "item" : "items"}
            </p>
          </div>
        </div>
        <Link to={`/console/migration/${resourceType}/create`}>
          <Button variant="primary" size="sm">
            <Plus className="size-3.5" /> {isTask ? "New import" : "Add connection"}
          </Button>
        </Link>
      </div>

      <Section>
        {items.length === 0 ? (
          <div className="py-12 text-center text-sm text-slate-500">
            {isTask
              ? "No imports yet. Add a connection first, then start an import."
              : "No connections yet. Add one to import machines from vSphere."}
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                  <th className="px-5 py-3">Name</th>
                  <th className="px-5 py-3">{isTask ? "Source" : "Endpoint"}</th>
                  <th className="px-5 py-3">{isTask ? "Machines" : "Type"}</th>
                  <th className="px-5 py-3">Status</th>
                  <th className="px-5 py-3">Age</th>
                  <th className="px-5 py-3 text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => {
                  const ready = readyCondition(item)
                  const statusText = isTask
                    ? (item.status?.phase ?? "Pending")
                    : ready?.status === "True"
                      ? "Ready"
                      : (ready?.reason ?? "Pending")
                  const statusTone =
                    statusText === "Succeeded" || statusText === "Ready" ? "ok" :
                    statusText === "Failed" ? "error" :
                    "warn"

                  const second = isTask ? item.spec?.sourceRef?.name : item.spec?.url
                  const third = isTask
                    ? `${item.spec?.vms?.length ?? 0}`
                    : (item.spec?.type ?? "-")

                  const progress = isTask ? taskProgress(item) : null

                  return (
                    <tr
                      key={item.metadata.name}
                      className="border-b border-slate-100 hover:bg-slate-50"
                    >
                      <td className="px-5 py-3 text-sm font-medium text-slate-900">
                        {isTask ? (
                          <Link
                            to={`/console/migration/vmimporttasks/${item.metadata.name}`}
                            className="text-blue-700 hover:underline"
                          >
                            {item.metadata.name}
                          </Link>
                        ) : (
                          item.metadata.name
                        )}
                      </td>
                      <td className="px-5 py-3 text-sm text-slate-600">{second ?? "-"}</td>
                      <td className="px-5 py-3 text-sm text-slate-600">{third}</td>
                      <td className="px-5 py-3">
                        <div className="flex items-center gap-2">
                          <span
                            className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${
                              statusTone === "ok"
                                ? "bg-green-100 text-green-800"
                                : statusTone === "error"
                                  ? "bg-red-100 text-red-800"
                                  : "bg-yellow-100 text-yellow-800"
                            }`}
                          >
                            {statusText}
                          </span>
                          {progress !== null && progress > 0 && progress < 100 && (
                            <span className="text-xs text-slate-500">{progress}%</span>
                          )}
                        </div>
                      </td>
                      <td className="px-5 py-3 text-sm text-slate-600">
                        {item.metadata.creationTimestamp
                          ? formatAge(item.metadata.creationTimestamp)
                          : "-"}
                      </td>
                      <td className="px-5 py-3 text-right">
                        <Button
                          variant="destructive"
                          size="sm"
                          onClick={() => handleDelete(item.metadata.name)}
                          disabled={deleteMutation.isPending}
                        >
                          <Trash2 className="size-3.5" /> Delete
                        </Button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Section>
    </div>
  )
}
