import { Navigate, Route, Routes } from "react-router"
import { ClusterUsagePage } from "./ClusterUsagePage.tsx"
import { ClusterUsageResourcePage } from "./ClusterUsageResourcePage.tsx"
import { StorageClassUsagePage } from "./StorageClassUsagePage.tsx"
import { StoragePage } from "./StoragePage.tsx"
import { NodesPage } from "./NodesPage.tsx"
import { BackupClassListPage } from "./BackupClassListPage.tsx"
import { BackupClassCreatePage } from "./BackupClassCreatePage.tsx"
import { BackupClassDetailPage } from "./BackupClassDetailPage.tsx"
import { BackupClassEditPage } from "./BackupClassEditPage.tsx"
import { BackupClassAdminGuard } from "./BackupClassAdminGuard.tsx"
import { CapacityAdminGuard } from "./CapacityAdminGuard.tsx"
import { InfoRedirect } from "./InfoRedirect.tsx"
import { ModulesPage } from "./ModulesPage.tsx"
import { ExternalIpsPage } from "./ExternalIpsPage.tsx"
import { TenantsPage } from "./TenantsPage.tsx"
import { ApplicationOrderPage } from "./ApplicationOrderPage.tsx"
import { ApplicationEditRoute } from "./detail/ApplicationEditRoute.tsx"
import { ApplicationDetailPage } from "./detail/ApplicationDetailPage.tsx"
import { ApplicationListPage } from "./ApplicationListPage.tsx"

/**
 * Admin portal at /admin/*. Administration (Info, Modules, External IPs,
 * Tenants) is always accessible, so the portal itself carries no gate. The two
 * cluster-wide operator areas keep their own independent permissions: Capacity
 * (nodes/list) and Backup Classes (backupclasses/update). Each is wrapped in a
 * layout guard that closes the direct-URL hole the sidebar already hides, so a
 * user without an area can never reach its pages even by typing the URL.
 */
export function AdminPage() {
  return (
    <Routes>
      <Route index element={<Navigate to="tenants" replace />} />
      <Route element={<CapacityAdminGuard />}>
        <Route path="capacity/cluster" element={<ClusterUsagePage />} />
        <Route path="capacity/cluster/r/*" element={<ClusterUsageResourcePage />} />
        <Route path="capacity/cluster/sc/*" element={<StorageClassUsagePage />} />
        <Route path="capacity/storage" element={<StoragePage />} />
        <Route path="capacity/nodes" element={<NodesPage />} />
      </Route>
      <Route element={<BackupClassAdminGuard />}>
        <Route path="backups/backupclasses" element={<BackupClassListPage />} />
        <Route path="backups/backupclasses/create" element={<BackupClassCreatePage />} />
        <Route path="backups/backupclasses/:name" element={<BackupClassDetailPage />} />
        <Route path="backups/backupclasses/:name/edit" element={<BackupClassEditPage />} />
      </Route>
      <Route path="info" element={<InfoRedirect />} />
      <Route path="modules" element={<ModulesPage />} />
      <Route path="external-ips" element={<ExternalIpsPage />} />
      <Route path="tenants" element={<TenantsPage />} />
      <Route path="new/:appName" element={<ApplicationOrderPage />} />
      <Route path=":plural/:name/edit" element={<ApplicationEditRoute />} />
      <Route path=":plural/:name/*" element={<ApplicationDetailPage />} />
      <Route path=":plural" element={<ApplicationListPage />} />
    </Routes>
  )
}
