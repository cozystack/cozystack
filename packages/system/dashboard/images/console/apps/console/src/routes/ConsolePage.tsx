import { Route, Routes } from "react-router"
import { ConsoleOverview } from "./ConsoleOverview.tsx"
import { ApplicationListPage } from "./ApplicationListPage.tsx"
import { ApplicationDetailPage } from "./detail/ApplicationDetailPage.tsx"
import { ApplicationEditRoute } from "./detail/ApplicationEditRoute.tsx"
import { BackupResourceListPage } from "./BackupResourceListPage.tsx"
import { BackupResourceEditPage } from "./BackupResourceEditPage.tsx"
import { BackupPlanCreatePage } from "./BackupPlanCreatePage.tsx"
import { BackupJobCreatePage } from "./BackupJobCreatePage.tsx"
import { BackupCreatePage } from "./BackupCreatePage.tsx"
import { BackupRestoreJobCreatePage } from "./BackupRestoreJobCreatePage.tsx"
import { MigrationResourceListPage } from "./MigrationResourceListPage.tsx"
import { MigrationSourceCreatePage } from "./MigrationSourceCreatePage.tsx"
import { MigrationTaskCreatePage } from "./MigrationTaskCreatePage.tsx"
import { MigrationTaskDetailPage } from "./MigrationTaskDetailPage.tsx"
import { ApplicationOrderPage } from "./ApplicationOrderPage.tsx"

export function ConsolePage() {
  return (
    <Routes>
      <Route index element={<ConsoleOverview />} />
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
      {/* Migration routes precede the generic `:plural` catch-alls below, which
          would otherwise swallow /console/migration/* as an application kind. */}
      <Route
        path="migration/vmimportsources"
        element={<MigrationResourceListPage resourceType="vmimportsources" title="Sources" />}
      />
      <Route
        path="migration/vmimportsources/create"
        element={<MigrationSourceCreatePage />}
      />
      <Route
        path="migration/vmimporttasks"
        element={<MigrationResourceListPage resourceType="vmimporttasks" title="Imports" />}
      />
      <Route
        path="migration/vmimporttasks/create"
        element={<MigrationTaskCreatePage />}
      />
      <Route
        path="migration/vmimporttasks/:name"
        element={<MigrationTaskDetailPage />}
      />
      <Route path="new/:appName" element={<ApplicationOrderPage />} />
      <Route path=":plural/:name/edit" element={<ApplicationEditRoute />} />
      <Route path=":plural/:name/*" element={<ApplicationDetailPage />} />
      <Route path=":plural" element={<ApplicationListPage />} />
    </Routes>
  )
}
