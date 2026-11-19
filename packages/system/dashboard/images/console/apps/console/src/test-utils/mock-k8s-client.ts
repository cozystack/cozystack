import { vi } from "vitest"
import { K8sClient, K8sApiError, type K8sList } from "@cozystack/k8s-client"

interface ListOverride {
  apiGroup: string
  apiVersion: string
  plural: string
  namespace?: string
  result: K8sList<unknown> | (() => K8sList<unknown> | Promise<K8sList<unknown>>) | K8sApiError
}

interface GetOverride {
  apiGroup: string
  apiVersion: string
  plural: string
  name: string
  namespace?: string
  result: unknown | (() => unknown | Promise<unknown>) | K8sApiError
}

export interface MockK8sClientOverrides {
  lists?: ListOverride[]
  gets?: GetOverride[]
}

/**
 * Build a K8sClient instance whose network-facing methods (list/get/watch)
 * resolve from in-memory overrides instead of fetch. The underlying object
 * is a real K8sClient so any method this factory does not stub — including
 * ones added to the production class after this file was written — falls
 * through to the real implementation; tests that touch new methods are
 * expected to spy on them explicitly via vi.spyOn on the returned instance.
 *
 * Watch is stubbed to return a noop cleanup function.
 */
export function createMockK8sClient(overrides: MockK8sClientOverrides = {}): K8sClient {
  const client = new K8sClient({ baseUrl: "/mock" })

  vi.spyOn(client, "list").mockImplementation(
    async (apiGroup, apiVersion, plural, namespace) => {
      const match = overrides.lists?.find(
        (o) =>
          o.apiGroup === apiGroup &&
          o.apiVersion === apiVersion &&
          o.plural === plural &&
          (o.namespace ?? undefined) === (namespace ?? undefined),
      )
      if (!match) {
        return { apiVersion, kind: `${plural}List`, metadata: {}, items: [] } as K8sList<unknown>
      }
      if (match.result instanceof K8sApiError) throw match.result
      const value = typeof match.result === "function" ? await match.result() : match.result
      return value as K8sList<unknown>
    },
  )

  vi.spyOn(client, "get").mockImplementation(
    async (apiGroup, apiVersion, plural, name, namespace) => {
      const match = overrides.gets?.find(
        (o) =>
          o.apiGroup === apiGroup &&
          o.apiVersion === apiVersion &&
          o.plural === plural &&
          o.name === name &&
          (o.namespace ?? undefined) === (namespace ?? undefined),
      )
      if (!match) {
        throw new K8sApiError(404, { message: `mock: no get override for ${plural}/${name}` })
      }
      if (match.result instanceof K8sApiError) throw match.result
      return typeof match.result === "function" ? await match.result() : match.result
    },
  )

  vi.spyOn(client, "watch").mockReturnValue(() => {})

  return client
}
