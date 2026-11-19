import { createContext, useContext, useMemo, useState, useEffect, type ReactNode } from "react"
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query"
import { K8sClient, type K8sClientConfig } from "./client.ts"

const K8sClientContext = createContext<K8sClient | null>(null)

const defaultQueryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: (failureCount, error) => {
        if (
          error &&
          "status" in error &&
          ((error as { status: number }).status === 401 ||
            (error as { status: number }).status === 403)
        )
          return false
        return failureCount < 3
      },
      retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 15000),
      refetchOnWindowFocus: false,
      refetchOnReconnect: true,
      placeholderData: (prev: unknown) => prev,
    },
  },
})

interface K8sProviderProps {
  config?: K8sClientConfig
  /**
   * Pre-built K8sClient instance. When supplied, replaces the client that
   * would otherwise be constructed from `config`. Primary use case: tests
   * that need to inject a mock; production code should pass `config` and
   * let the provider build the real client.
   */
  client?: K8sClient
  queryClient?: QueryClient
  children: ReactNode
}

export function K8sProvider({ config, client, queryClient, children }: K8sProviderProps) {
  const resolved = useMemo(() => client ?? new K8sClient(config), [client, config])
  const qc = queryClient ?? defaultQueryClient

  return (
    <QueryClientProvider client={qc}>
      <K8sClientContext.Provider value={resolved}>{children}</K8sClientContext.Provider>
    </QueryClientProvider>
  )
}

export function useConnectionError(): boolean {
  const queryClient = useQueryClient()
  const [hasError, setHasError] = useState(false)

  useEffect(() => {
    const cache = queryClient.getQueryCache()
    const isConnectionError = (err: unknown) => {
      if (!err || typeof err !== "object") return false
      const status = (err as { status?: number }).status
      if (status === 401 || status === 403) return false
      return true
    }
    const check = () => {
      const queries = cache.getAll()
      const anyFailing = queries.some(
        (q) => q.state.error != null && isConnectionError(q.state.error),
      )
      const allHealthy =
        queries.length > 0 &&
        queries.every((q) => q.state.error == null || !isConnectionError(q.state.error))
      if (allHealthy) setHasError(false)
      else if (anyFailing) setHasError(true)
    }
    const unsubscribe = cache.subscribe(check)
    return unsubscribe
  }, [queryClient])

  return hasError
}

export function useK8sClient(): K8sClient {
  const client = useContext(K8sClientContext)
  if (!client) {
    throw new Error("useK8sClient must be used within a K8sProvider")
  }
  return client
}
