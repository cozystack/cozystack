import { useQuery } from "@tanstack/react-query"
import { useK8sClient } from "./provider.tsx"
import type { K8sResource } from "./client.ts"

export interface ResourceAttributes {
  namespace?: string
  verb?: string
  group?: string
  version?: string
  resource?: string
  subresource?: string
  name?: string
}

export interface NonResourceAttributes {
  path?: string
  verb?: string
}

export interface SelfSubjectAccessReviewSpec {
  resourceAttributes?: ResourceAttributes
  nonResourceAttributes?: NonResourceAttributes
}

interface SelfSubjectAccessReviewStatus {
  allowed: boolean
  denied?: boolean
  reason?: string
  evaluationError?: string
}

export type SelfSubjectAccessReview = K8sResource<
  SelfSubjectAccessReviewSpec,
  SelfSubjectAccessReviewStatus
>

function ssarCacheKey(spec: SelfSubjectAccessReviewSpec): readonly string[] {
  const r = spec.resourceAttributes ?? {}
  const n = spec.nonResourceAttributes ?? {}
  return [
    "ssar",
    r.namespace ?? "",
    r.group ?? "",
    r.version ?? "",
    r.resource ?? "",
    r.subresource ?? "",
    r.verb ?? "",
    r.name ?? "",
    n.path ?? "",
    n.verb ?? "",
  ]
}

/**
 * Issues a SelfSubjectAccessReview against the cluster and returns
 * whether the current user is allowed to perform the requested action.
 * The result is cached per spec for the lifetime of the QueryClient
 * (staleTime: Infinity, refetchOnWindowFocus disabled) — discovery-style
 * permission checks rarely change mid-session, and a revoked grant
 * surfaces on the next page load.
 *
 * Errors are absorbed and surfaced as allowed=false. Callers asking
 * 'can I see this UI section' are functionally indifferent to 'denied'
 * versus 'cannot determine'; in both cases the section stays hidden.
 */
export function useSelfSubjectAccessReview(
  spec: SelfSubjectAccessReviewSpec,
): { allowed: boolean; isLoading: boolean; error: Error | null } {
  const client = useK8sClient()
  const queryKey = ssarCacheKey(spec)
  const query = useQuery<SelfSubjectAccessReview>({
    queryKey,
    queryFn: () =>
      client.create<SelfSubjectAccessReview>(
        "authorization.k8s.io",
        "v1",
        "selfsubjectaccessreviews",
        {
          apiVersion: "authorization.k8s.io/v1",
          kind: "SelfSubjectAccessReview",
          metadata: { name: "" },
          spec,
        },
      ),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })
  return {
    allowed: query.data?.status?.allowed ?? false,
    isLoading: query.isLoading,
    error: (query.error as Error | null) ?? null,
  }
}
