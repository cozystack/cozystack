import { useSelfSubjectAccessReview } from "@cozystack/k8s-client"

// BackupClass is cluster-scoped, and tenants already hold get/list/watch on it,
// so a read gate would not exclude them — only write does. Gating on `update`
// is what makes the Backup Classes area admin-only. Fail closed: loading and
// error states resolve as "not allowed" so the sidebar entry never flickers in
// then out.
export function useBackupClassAdminAccess(): { allowed: boolean; isLoading: boolean } {
  const review = useSelfSubjectAccessReview({
    resourceAttributes: {
      group: "backups.cozystack.io",
      resource: "backupclasses",
      verb: "update",
    },
  })
  return {
    isLoading: review.isLoading,
    allowed: !review.isLoading && !review.error && review.allowed,
  }
}
