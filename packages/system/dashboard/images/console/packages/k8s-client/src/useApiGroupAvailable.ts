import { useQuery } from "@tanstack/react-query"
import { useK8sClient } from "./provider.tsx"
import type { APIGroupList } from "./client.ts"

/**
 * Returns whether a specific Kubernetes API group is registered on the
 * cluster, derived from a single `/apis` discovery call shared across
 * all consumers. Result is cached for the lifetime of the QueryClient
 * (staleTime: Infinity) — discovery is rarely meaningful to re-poll
 * within a session, and callers can invalidate the "k8s-api-groups"
 * query key explicitly if the use case ever arises.
 *
 * Errors are absorbed silently and reported as available=false: the
 * caller is asking a discovery question, and "we cannot find out" is
 * functionally identical to "not registered" for the cluster-usage
 * page's gating logic.
 */
export function useApiGroupAvailable(
  groupName: string,
): { available: boolean; isLoading: boolean } {
  const client = useK8sClient()
  const query = useQuery<APIGroupList>({
    queryKey: ["k8s-api-groups"],
    queryFn: () => client.getApiGroups(),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })
  const available = query.data?.groups.some((g) => g.name === groupName) ?? false
  return { available, isLoading: query.isLoading }
}
