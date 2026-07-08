import { useSelfSubjectAccessReview } from "@cozystack/k8s-client"

// The Capacity area (Cluster / Nodes / Storage and the per-resource
// drill-downs) reads cluster-scoped objects — nodes, and pods/PVCs across every
// tenant namespace — that tenant users cannot list. Gate the whole area on
// `nodes/list` as the "cluster operator" proxy. Fail closed: loading and error
// resolve as "not allowed" so the sidebar entry never flickers in then out.
export function useClusterUsageAccess(): { allowed: boolean; isLoading: boolean } {
  const review = useSelfSubjectAccessReview({
    resourceAttributes: { resource: "nodes", verb: "list" },
  })
  return {
    isLoading: review.isLoading,
    allowed: !review.isLoading && !review.error && review.allowed,
  }
}
