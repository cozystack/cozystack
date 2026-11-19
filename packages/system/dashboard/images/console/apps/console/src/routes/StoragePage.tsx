import { ClusterStorageSection } from "../components/cluster-usage/ClusterStorageSection.tsx"

/**
 * Admin → Capacity → Storage. PersistentVolumeClaims across tenant namespaces
 * aggregated by StorageClass; each class drills down to the consuming
 * workloads. Split out of the Cluster page onto its own tab.
 */
export function StoragePage() {
  return (
    <div className="space-y-6 p-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">Storage</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          PersistentVolumeClaims across all tenants, grouped by StorageClass.
        </p>
      </div>
      <ClusterStorageSection />
    </div>
  )
}
