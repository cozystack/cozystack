import { Link, Outlet } from "react-router"
import { Section, Spinner } from "@cozystack/ui"
import { useBackupClassAdminAccess } from "../hooks/useBackupClassAdminAccess.ts"

/**
 * Layout route guard for the Backup Classes pages. Renders the matched child
 * route only for users who may update backup classes; everyone else gets a
 * permission-denied message with a link back to the console instead of the
 * page (and instead of a browser 403 on direct URL navigation).
 */
export function BackupClassAdminGuard() {
  const { allowed, isLoading } = useBackupClassAdminAccess()

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 p-6 text-sm text-slate-500">
        <Spinner /> Loading…
      </div>
    )
  }

  if (!allowed) {
    return (
      <div className="p-6">
        <Section>
          <div className="px-2 py-4 text-sm text-slate-700">
            You do not have permission to manage backup classes.{" "}
            <Link to="/console" className="text-blue-700 underline hover:text-blue-800">
              Back to console
            </Link>
            .
          </div>
        </Section>
      </div>
    )
  }

  return <Outlet />
}
