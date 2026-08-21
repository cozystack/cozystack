import { Link, useParams } from "react-router"
import { Import } from "lucide-react"
import { Section, Spinner } from "@cozystack/ui"
import { useK8sGet } from "@cozystack/k8s-client"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { formatAge } from "../lib/status.ts"
import {
  MIGRATION_GROUP,
  MIGRATION_VERSION,
  type MigrationResource,
  phaseTone,
  taskProgress,
} from "../lib/migration.ts"

function PhaseBadge({ phase }: { phase: string | undefined }) {
  const tone = phaseTone(phase)
  return (
    <span
      className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${
        tone === "ok"
          ? "bg-green-100 text-green-800"
          : tone === "error"
            ? "bg-red-100 text-red-800"
            : "bg-yellow-100 text-yellow-800"
      }`}
    >
      {phase ?? "Pending"}
    </span>
  )
}

export function MigrationTaskDetailPage() {
  const { name } = useParams<{ name: string }>()
  const { tenantNamespace } = useTenantContext()

  const { data: task, isLoading, error } = useK8sGet<MigrationResource>(
    {
      apiGroup: MIGRATION_GROUP,
      apiVersion: MIGRATION_VERSION,
      plural: "vmimporttasks",
      name: name ?? "",
      namespace: tenantNamespace ?? "",
    },
    { enabled: !!name && !!tenantNamespace },
  )

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 p-6 text-sm text-slate-500">
        <Spinner /> Loading…
      </div>
    )
  }

  if (error || !task) {
    return (
      <div className="p-6 text-sm text-red-600">
        Failed to load import {name}: {(error as Error | null)?.message ?? "not found"}
      </div>
    )
  }

  const vms = task.status?.vms ?? []
  const overall = taskProgress(task)

  return (
    <div className="p-6">
      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
          <Import className="size-6 text-slate-600" />
        </div>
        <div>
          <h1 className="text-lg font-semibold text-slate-900">{task.metadata.name}</h1>
          <p className="text-xs text-slate-500">
            from{" "}
            <Link
              to="/console/migration/vmimportsources"
              className="text-blue-700 hover:underline"
            >
              {task.spec?.sourceRef?.name ?? "-"}
            </Link>
            {task.metadata.creationTimestamp
              ? ` · started ${formatAge(task.metadata.creationTimestamp)} ago`
              : ""}
          </p>
        </div>
        <div className="ml-auto flex items-center gap-3">
          {overall !== null && overall < 100 && (
            <span className="text-sm text-slate-500">{overall}%</span>
          )}
          <PhaseBadge phase={task.status?.phase} />
        </div>
      </div>

      {task.status?.message && (
        <div
          className={`mb-4 rounded-lg border px-3 py-2 text-xs ${
            task.status.phase === "Failed"
              ? "border-red-200 bg-red-50 text-red-800"
              : "border-slate-200 bg-slate-50 text-slate-600"
          }`}
        >
          {task.status.message}
        </div>
      )}

      <Section>
        {vms.length === 0 ? (
          <div className="py-12 text-center text-sm text-slate-500">
            Nothing has started yet. The import waits until its connection is ready.
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                  <th className="px-5 py-3">Source machine</th>
                  <th className="px-5 py-3">Phase</th>
                  <th className="px-5 py-3">Progress</th>
                  <th className="px-5 py-3">Virtual machine</th>
                  <th className="px-5 py-3">Disks</th>
                </tr>
              </thead>
              <tbody>
                {vms.map((vm) => (
                  <tr key={vm.id} className="border-b border-slate-100">
                    <td className="px-5 py-3 text-sm text-slate-900">
                      <div className="font-medium">{vm.name || vm.id}</div>
                      {vm.name && <div className="font-mono text-xs text-slate-500">{vm.id}</div>}
                    </td>
                    <td className="px-5 py-3">
                      <PhaseBadge phase={vm.phase} />
                      {vm.message && (
                        <div className="mt-1 max-w-md text-xs text-slate-500">{vm.message}</div>
                      )}
                    </td>
                    <td className="px-5 py-3">
                      <div className="flex items-center gap-2">
                        <div className="h-1.5 w-24 overflow-hidden rounded-full bg-slate-200">
                          <div
                            className={`h-full rounded-full ${
                              vm.phase === "Failed" ? "bg-red-400" : "bg-blue-500"
                            }`}
                            style={{ width: `${Math.min(100, Math.max(0, vm.progress ?? 0))}%` }}
                          />
                        </div>
                        <span className="text-xs text-slate-500">{vm.progress ?? 0}%</span>
                      </div>
                    </td>
                    <td className="px-5 py-3 text-sm text-slate-600">
                      {vm.vmInstance ? (
                        <Link
                          to={`/console/vminstances/${vm.vmInstance}`}
                          className="text-blue-700 hover:underline"
                        >
                          {vm.vmInstance}
                        </Link>
                      ) : (
                        "-"
                      )}
                    </td>
                    <td className="px-5 py-3 text-sm text-slate-600">
                      {vm.disks && vm.disks.length > 0 ? (
                        <div className="flex flex-col gap-0.5">
                          {vm.disks.map((disk) => (
                            <Link
                              key={disk}
                              to={`/console/vmdisks/${disk}`}
                              className="text-blue-700 hover:underline"
                            >
                              {disk}
                            </Link>
                          ))}
                        </div>
                      ) : (
                        "-"
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      {/* Stated on the page rather than only in docs: a tenant who is unsure
          whether cleaning up destroys their machines will never clean up. */}
      <p className="mt-4 text-xs text-slate-500">
        Imported machines and disks are ordinary Cozystack objects. Deleting this import
        removes only the migration machinery.
      </p>
    </div>
  )
}
