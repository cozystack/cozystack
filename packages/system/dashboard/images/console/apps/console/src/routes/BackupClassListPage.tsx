import { Link } from "react-router"
import { Archive, ChevronRight, Plus } from "lucide-react"
import { Button, Section, Spinner, StatusBadge } from "@cozystack/ui"
import { useK8sGet, useK8sList, type K8sResource } from "@cozystack/k8s-client"
import type { ApplicationInstance } from "@cozystack/types"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { useApplicationDefinitions, iconDataUrl } from "../lib/app-definitions.ts"
import { formatAge, readyCondition } from "../lib/status.ts"

// Hardcoded: cozy-backups in tenant-root is the cluster's system backup
// bucket. Swap this for a discovery hook once cozystack exposes a canonical
// reference (e.g. via a status field on BackupClass or a known label).
function SystemBackupBucketPanel() {
  const { selectTenant } = useTenantContext()
  const { data: bucket, isLoading } = useK8sGet<ApplicationInstance>({
    apiGroup: "apps.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "buckets",
    name: "cozy-backups",
    namespace: "tenant-root",
  })
  // Reuse the Bucket ApplicationDefinition's own icon (the same red S3
  // glyph rendered on deployed-app cards) so the bucket here is visually
  // recognisable as a Bucket app.
  const { data: appDefs } = useApplicationDefinitions()
  const bucketAd = appDefs?.items.find((d) => d.spec?.application.kind === "Bucket")
  const bucketIcon = bucketAd ? iconDataUrl(bucketAd) : undefined

  if (isLoading) {
    return (
      <Section className="h-full" bodyClassName="p-0">
        <div className="flex h-full items-center gap-2 p-4 text-sm text-slate-500">
          <Spinner /> Loading system backup bucket…
        </div>
      </Section>
    )
  }

  // Bucket missing (or unreachable) — surface as an attention-grabbing
  // placeholder rather than hiding, so an admin can't miss the gap.
  if (!bucket) {
    return (
      <Section className="h-full" bodyClassName="p-0">
        <div className="flex h-full items-center justify-between gap-4 p-4">
          <div className="flex items-center gap-3">
            <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-amber-50">
              <Archive className="size-6 text-amber-600" />
            </div>
            <div>
              <div className="text-sm font-medium text-slate-900">cozy-backups</div>
              <div className="text-xs text-slate-500">
                Not deployed on this cluster
              </div>
            </div>
          </div>
          <StatusBadge tone="warn">Not configured</StatusBadge>
        </div>
      </Section>
    )
  }

  const ready = readyCondition(bucket)
  const tone = ready?.status === "True" ? "ok" : ready ? "warn" : "muted"
  const label =
    ready?.status === "True" ? "Ready" : (ready?.reason ?? "Unknown")

  return (
    <Section className="h-full" bodyClassName="p-0">
      <Link
        to="/console/buckets/cozy-backups"
        onClick={() => selectTenant("root")}
        className="flex h-full items-center justify-between gap-4 p-4 hover:bg-slate-50"
      >
        <div className="flex items-center gap-3">
          <div className="flex size-11 shrink-0 items-center justify-center overflow-hidden rounded-md bg-slate-100">
            {bucketIcon ? (
              <img src={bucketIcon} alt="" className="h-full w-full" />
            ) : (
              <Archive className="size-6 text-slate-600" />
            )}
          </div>
          <div>
            <div className="text-sm font-medium text-slate-900">cozy-backups</div>
            <div className="text-xs text-slate-500">
              System backup bucket · {formatAge(bucket.metadata.creationTimestamp)}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <StatusBadge tone={tone}>{label}</StatusBadge>
          <ChevronRight className="size-4 text-slate-400" />
        </div>
      </Link>
    </Section>
  )
}

// Cluster-wide count of backup artifacts. List call omits the namespace so it
// hits the all-namespaces endpoint; the click target is the per-tenant list
// page, which scopes to whatever tenant the admin has selected.
function BackupArtifactsCountPanel() {
  const { data, isLoading } = useK8sList<K8sResource>({
    apiGroup: "backups.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "backups",
  })

  if (isLoading) {
    return (
      <Section className="h-full" bodyClassName="p-0">
        <div className="flex h-full items-center gap-2 p-4 text-sm text-slate-500">
          <Spinner /> Loading backup artifacts…
        </div>
      </Section>
    )
  }

  const count = data?.items?.length ?? 0

  return (
    <Section className="h-full" bodyClassName="p-0">
      <Link
        to="/console/backups/backups"
        className="flex h-full items-center justify-between gap-4 p-4 hover:bg-slate-50"
      >
        <div className="flex items-center gap-3">
          <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
            <Archive className="size-6 text-slate-600" />
          </div>
          <div>
            <div className="text-lg font-semibold tabular-nums text-slate-900">{count}</div>
            <div className="text-xs text-slate-500">Backup artifacts count</div>
          </div>
        </div>
        <ChevronRight className="size-4 text-slate-400" />
      </Link>
    </Section>
  )
}

export function BackupClassListPage() {
  // BackupClass is cluster-scoped — not tenant-namespaced.
  const { data, isLoading, error } = useK8sList<K8sResource>({
    apiGroup: "backups.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "backupclasses",
  })

  const items = data?.items ?? []

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
        Failed to load Backup Classes: {(error as Error).message}
      </div>
    )
  }

  return (
    <div className="p-6">
      <div className="mb-5">
        <h1 className="text-lg font-semibold text-slate-900">Backups</h1>
        <p className="text-xs text-slate-500">System storage and classes configuration</p>
      </div>

      <div className="mb-4 grid gap-4 md:grid-cols-2">
        <SystemBackupBucketPanel />
        <BackupArtifactsCountPanel />
      </div>

      <Section>
        <div className="flex items-center justify-between gap-4 border-b border-slate-200 px-5 py-3">
          <div>
            <h2 className="text-sm font-medium text-slate-900">Backup Classes</h2>
            <p className="text-xs text-slate-500">
              {items.length} {items.length === 1 ? "item" : "items"}
            </p>
          </div>
          <Link to="/admin/backups/backupclasses/create">
            <Button variant="primary" size="sm">
              <Plus className="size-3.5" /> Create Backup Class
            </Button>
          </Link>
        </div>
        {items.length === 0 ? (
          <div className="py-12 text-center text-sm text-slate-500">
            No backup classes found.
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                  <th className="px-5 py-3">Name</th>
                  <th className="px-5 py-3">Age</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => (
                  <tr
                    key={item.metadata.name}
                    className="border-b border-slate-100 hover:bg-slate-50"
                  >
                    <td className="px-5 py-3 text-sm font-medium">
                      <Link
                        to={`/admin/backups/backupclasses/${item.metadata.name}`}
                        className="text-blue-600 hover:underline"
                      >
                        {item.metadata.name}
                      </Link>
                    </td>
                    <td className="px-5 py-3 text-sm text-slate-600">
                      {item.metadata.creationTimestamp
                        ? formatAge(item.metadata.creationTimestamp)
                        : "-"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>
    </div>
  )
}
