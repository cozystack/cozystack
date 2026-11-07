import { render, type RenderResult } from "@testing-library/react"
import { QueryClient } from "@tanstack/react-query"
import { K8sProvider, type K8sClient } from "@cozystack/k8s-client"
import { MemoryRouter } from "react-router"
import type { ReactElement } from "react"
import { AppConfigContext, type AppConfig } from "../lib/config.ts"

export interface RenderWithK8sOptions {
  client: K8sClient
  initialRoute?: string
  /** Console config, e.g. { vmSubstrate: "proxmox" }. */
  config?: AppConfig
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
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  const result = render(
    <K8sProvider client={options.client} queryClient={queryClient}>
      <AppConfigContext.Provider value={options.config ?? {}}>
        <MemoryRouter initialEntries={[options.initialRoute ?? "/"]}>{ui}</MemoryRouter>
      </AppConfigContext.Provider>
    </K8sProvider>,
  )
  return Object.assign(result, { queryClient })
}
