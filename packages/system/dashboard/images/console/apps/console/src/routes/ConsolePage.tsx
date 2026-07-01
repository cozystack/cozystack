import { Route, Routes } from "react-router"
import { ConsoleOverview } from "./ConsoleOverview.tsx"
import { TenantsPage } from "./TenantsPage.tsx"
import { ModulesPage } from "./ModulesPage.tsx"
import { ExternalIpsPage } from "./ExternalIpsPage.tsx"
import { InfoRedirect } from "./InfoRedirect.tsx"
import { ApplicationListPage } from "./ApplicationListPage.tsx"
import { ApplicationDetailPage } from "./detail/ApplicationDetailPage.tsx"
import { ApplicationEditRoute } from "./detail/ApplicationEditRoute.tsx"
import { BackupResourceListPage } from "./BackupResourceListPage.tsx"
import { BackupResourceEditPage } from "./BackupResourceEditPage.tsx"
import { BackupPlanCreatePage } from "./BackupPlanCreatePage.tsx"
import { BackupJobCreatePage } from "./BackupJobCreatePage.tsx"
import { BackupCreatePage } from "./BackupCreatePage.tsx"
import { BackupRestoreJobCreatePage } from "./BackupRestoreJobCreatePage.tsx"
import { ApplicationOrderPage } from "./ApplicationOrderPage.tsx"

export function ConsolePage() {
  return (
    <Routes>
      <Route index element={<ConsoleOverview />} />
      <Route path="info" element={<InfoRedirect />} />
      <Route path="modules" element={<ModulesPage />} />
      <Route path="external-ips" element={<ExternalIpsPage />} />
      <Route path="tenants" element={<TenantsPage />} />
      <Route
        path="backups/plans"
        element={<BackupResourceListPage resourceType="plans" title="Plans" />}
      />
      <Route
        path="backups/plans/create"
        element={<BackupPlanCreatePage />}
      />
      <Route
        path="backups/plans/:name/edit"
        element={<BackupResourceEditPage resourceType="plans" title="Plans" />}
      />
      <Route
        path="backups/backupjobs"
        element={<BackupResourceListPage resourceType="backupjobs" title="Backup Jobs" />}
      />
      <Route
        path="backups/backupjobs/create"
        element={<BackupJobCreatePage />}
      />
      <Route
        path="backups/backupjobs/:name/edit"
        element={<BackupResourceEditPage resourceType="backupjobs" title="Backup Jobs" />}
      />
      <Route
        path="backups/backups"
        element={<BackupResourceListPage resourceType="backups" title="Backups" />}
      />
      <Route
        path="backups/backups/create"
        element={<BackupCreatePage />}
      />
      <Route
        path="backups/backups/:name/edit"
        element={<BackupResourceEditPage resourceType="backups" title="Backups" />}
      />
      <Route
        path="backups/restorejobs"
        element={<BackupResourceListPage resourceType="restorejobs" title="Restore Jobs" />}
      />
      <Route
        path="backups/restorejobs/create"
        element={<BackupRestoreJobCreatePage />}
      />
      <Route
        path="backups/restorejobs/:name/edit"
        element={<BackupResourceEditPage resourceType="restorejobs" title="Restore Jobs" />}
      />
      <Route path="new/:appName" element={<ApplicationOrderPage />} />
      <Route path=":plural/:name/edit" element={<ApplicationEditRoute />} />
      <Route path=":plural/:name/*" element={<ApplicationDetailPage />} />
      <Route path=":plural" element={<ApplicationListPage />} />
    </Routes>
  )
}
