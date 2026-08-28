import { render, type RenderResult } from "@testing-library/react"
import { QueryClient } from "@tanstack/react-query"
import { K8sProvider, type K8sClient } from "@cozystack/k8s-client"
import { MemoryRouter } from "react-router"
import type { ReactElement } from "react"

export interface RenderWithK8sOptions {
  client: K8sClient
  initialRoute?: string
}

/**
 * Wraps a React tree in the minimum context needed to exercise K8s hooks
 * in isolation: a fresh QueryClient with retries off and no garbage
 * collection, the K8sProvider with the injected client, and a
 * MemoryRouter so components that use react-router do not blow up.
 */
export function renderWithK8sProvider(
  ui: ReactElement,
  options: RenderWithK8sOptions,
): RenderResult & { queryClient: QueryClient } {
  // Mirror the production defaults in `K8sProvider` that change what a
  // component observes: `placeholderData` serves the previous query's data
  // with status "success" when the key changes, so a test client without it
  // never sees the window where `isLoading` is false but the data is stale.
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        gcTime: 0,
        staleTime: 30_000,
        placeholderData: (prev: unknown) => prev,
      },
    },
  })
  const result = render(
    <K8sProvider client={options.client} queryClient={queryClient}>
      <MemoryRouter initialEntries={[options.initialRoute ?? "/"]}>{ui}</MemoryRouter>
    </K8sProvider>,
  )
  return Object.assign(result, { queryClient })
}
