import {
  useQuery,
  useMutation,
  useQueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useK8sClient } from "./provider.tsx"
import { K8sApiError, type K8sList, type K8sResource } from "./client.ts"

export interface ResourceRef {
  apiGroup: string
  apiVersion: string
  plural: string
  namespace?: string
}

export function useK8sList<T extends K8sResource>(
  ref: ResourceRef,
  options?: Omit<UseQueryOptions<K8sList<T>>, "queryKey" | "queryFn"> & {
    watch?: boolean
    labelSelector?: string
    fieldSelector?: string
  },
) {
  const client = useK8sClient()
  const queryClient = useQueryClient()
  const { labelSelector, fieldSelector, watch: watchOpt, ...queryOptions } = options ?? {}
  const queryKey = useMemo(
    () =>
      k8sListKey(
        {
          apiGroup: ref.apiGroup,
          apiVersion: ref.apiVersion,
          plural: ref.plural,
          namespace: ref.namespace,
        },
        labelSelector,
        fieldSelector,
      ),
    [
      ref.apiGroup,
      ref.apiVersion,
      ref.plural,
      ref.namespace,
      labelSelector,
      fieldSelector,
    ],
  )
  const enabled = queryOptions.enabled !== false
  const watchEnabled = watchOpt !== false
  const query = useQuery<K8sList<T>>({
    queryKey,
    queryFn: () =>
      client.list<T>(ref.apiGroup, ref.apiVersion, ref.plural, ref.namespace, {
        labelSelector,
        fieldSelector,
      }),
    ...queryOptions,
  })

  const hasResourceVersion = !!query.data?.metadata?.resourceVersion

  const [restart, setRestart] = useState({ key: queryKey, generation: 0 })
  const restartWatch = useCallback(() => {
    setRestart((old) => ({ key: queryKey, generation: old.generation + 1 }))
  }, [queryKey])
  const watchKey = useMemo(
    () => ({ queryKey, enabled, watchEnabled, hasResourceVersion, generation: restart.generation }),
    [queryKey, enabled, watchEnabled, hasResourceVersion, restart.generation],
  )
  const [watchState, setWatchState] = useState<{
    key: typeof watchKey
    ready: boolean
    error?: Error
  }>({ key: watchKey, ready: false })

  useEffect(() => {
    if (!enabled || !watchEnabled || !hasResourceVersion) return
    let active = true
    let generation = 0
    let attempts = 0
    let abort: (() => void) | undefined
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    let healthyTimer: ReturnType<typeof setTimeout> | undefined

    const stop = () => {
      generation++
      clearTimeout(retryTimer)
      clearTimeout(healthyTimer)
      retryTimer = undefined
      healthyTimer = undefined
      abort?.()
      abort = undefined
    }
    const fail = (error: Error) => {
      if (!active) return
      stop()
      setWatchState({ key: watchKey, ready: false, error })
      const status = error instanceof K8sApiError ? error.status : 0
      if (status >= 400 && status < 500 && status !== 410 && status !== 429) return
      const delay = Math.min(1000 * 2 ** attempts, 30000)
      attempts = Math.min(attempts + 1, 5)
      retryTimer = setTimeout(() => { void relist() }, delay)
    }
    const open = (resourceVersion: string) => {
      const current = generation
      const isCurrent = () => active && current === generation
      const cleanup = client.watch<T>(
        ref.apiGroup, ref.apiVersion, ref.plural, ref.namespace, resourceVersion,
        (event) => {
          if (!isCurrent() || event.type === "BOOKMARK") return
          if (event.type === "ERROR") {
            fail(new K8sApiError(event.object.code ?? 0, event.object))
            return
          }
          queryClient.setQueryData<K8sList<T>>(queryKey, (old) => {
            if (!old) return old
            const items = [...old.items]
            const idx = items.findIndex(
              (i) => i.metadata.name === event.object.metadata.name &&
                i.metadata.namespace === event.object.metadata.namespace,
            )
            switch (event.type) {
              case "ADDED":
              case "MODIFIED":
                if (idx === -1) items.push(event.object)
                else items[idx] = event.object
                break
              case "DELETED":
                if (idx >= 0) items.splice(idx, 1)
                break
            }
            return { ...old, items }
          })
        },
        (error) => { if (isCurrent()) fail(error) },
        {
          labelSelector,
          fieldSelector,
          onOpen: () => {
            if (!isCurrent()) return
            setWatchState({ key: watchKey, ready: true })
            healthyTimer = setTimeout(() => { attempts = 0 }, 30000)
          },
        },
      )
      if (isCurrent()) abort = cleanup
      else cleanup()
    }
    const relist = async () => {
      stop()
      const current = generation
      try {
        const data = await queryClient.fetchQuery({
          queryKey,
          queryFn: () => client.list<T>(ref.apiGroup, ref.apiVersion, ref.plural, ref.namespace, {
            labelSelector, fieldSelector,
          }),
          retry: false,
          staleTime: 0,
        })
        if (!active || current !== generation) return
        if (!data.metadata.resourceVersion) throw new Error("List response has no resourceVersion")
        // Reopen directly: a successful relist can have the same resourceVersion.
        open(data.metadata.resourceVersion)
      } catch (error) {
        if (active && current === generation) {
          fail(error instanceof Error ? error : new Error(String(error)))
        }
      }
    }
    if (restart.key === queryKey && restart.generation > 0) {
      void relist()
    } else {
      const resourceVersion = queryClient.getQueryData<K8sList<T>>(queryKey)?.metadata.resourceVersion
      if (resourceVersion) open(resourceVersion)
    }

    return () => {
      active = false
      stop()
    }
  }, [
    hasResourceVersion, enabled, watchEnabled, queryKey, watchKey, restart, client, queryClient,
    ref.apiGroup, ref.apiVersion, ref.plural, ref.namespace, labelSelector, fieldSelector,
  ])

  // Keep React Query's tracked result proxy intact; spreading subscribes to every property.
  return Object.assign(query, {
    watchReady: enabled && watchEnabled && watchState.key === watchKey && watchState.ready,
    watchError: watchState.key === watchKey ? watchState.error : undefined,
    restartWatch,
  })
}

export function useK8sGet<T extends K8sResource>(
  ref: ResourceRef & { name: string },
  options?: Omit<UseQueryOptions<T>, "queryKey" | "queryFn">,
) {
  const client = useK8sClient()
  const queryKey = k8sGetKey(ref)

  return useQuery<T>({
    queryKey,
    queryFn: () =>
      client.get<T>(ref.apiGroup, ref.apiVersion, ref.plural, ref.name, ref.namespace),
    ...options,
  })
}

export function useK8sCreate<T extends K8sResource>(ref: ResourceRef) {
  const client = useK8sClient()
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (body: T) =>
      client.create<T>(ref.apiGroup, ref.apiVersion, ref.plural, body, ref.namespace),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: k8sListKey(ref) })
    },
  })
}

export function useK8sUpdate<T extends K8sResource>(ref: ResourceRef) {
  const client = useK8sClient()
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (body: T) =>
      client.update<T>(
        ref.apiGroup,
        ref.apiVersion,
        ref.plural,
        body.metadata.name,
        body,
        ref.namespace,
      ),
    onSuccess: (data, variables) => {
      const getKey = k8sGetKey({ ...ref, name: variables.metadata.name })
      queryClient.setQueryData(getKey, data)
      queryClient.invalidateQueries({ queryKey: k8sListKey(ref) })
    },
  })
}

export function useK8sDelete(ref: ResourceRef) {
  const client = useK8sClient()
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (name: string) =>
      client.delete(ref.apiGroup, ref.apiVersion, ref.plural, name, ref.namespace),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: k8sListKey(ref) })
    },
  })
}

/**
 * Mutation hook for calling a resource subresource action (e.g. KubeVirt
 * virtualmachines/{name}/start|stop|restart). On success it invalidates every
 * GET and LIST cache for the target resource so its status (e.g.
 * printableStatus) refetches.
 *
 * The action endpoint and the resource whose status you want to refresh can
 * live under different API groups — KubeVirt serves the actions under
 * `subresources.kubevirt.io` but the VirtualMachine (with its status) under
 * `kubevirt.io`. Pass `options.invalidate` with the target resource's ref so
 * the invalidation hits the query that holds the status; without it the keys
 * never match and the refresh does nothing.
 *
 * Invalidation keys off the resource prefix `["k8s", group, version, plural,
 * namespace]`, which React Query prefix-matches against both the by-name GET
 * key and any field/label-selected LIST key — so a status read via a
 * `metadata.name` field-selected `useK8sList` (the watch-based, no-poll path)
 * is refreshed too.
 */
export function useK8sSubresource(
  ref: ResourceRef & { name: string },
  options?: { invalidate?: ResourceRef },
) {
  const client = useK8sClient()
  const queryClient = useQueryClient()
  const invalidateRef = options?.invalidate ?? ref

  return useMutation({
    mutationFn: ({
      subresource,
      body,
      method,
    }: {
      subresource: string
      body?: unknown
      method?: "PUT" | "POST"
    }) =>
      client.subresource(
        ref.apiGroup,
        ref.apiVersion,
        ref.plural,
        ref.name,
        subresource,
        ref.namespace,
        body ?? {},
        method,
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: k8sResourceKey(invalidateRef) })
    },
  })
}

/**
 * Prefix shared by every GET and LIST key for a resource in a namespace.
 * React Query prefix-matches on this, so invalidating it refreshes the by-name
 * GET and every selector-scoped LIST of that resource at once.
 */
function k8sResourceKey(ref: ResourceRef) {
  return ["k8s", ref.apiGroup, ref.apiVersion, ref.plural, ref.namespace ?? ""] as const
}

function k8sListKey(ref: ResourceRef, labelSelector?: string, fieldSelector?: string) {
  return [
    "k8s",
    ref.apiGroup,
    ref.apiVersion,
    ref.plural,
    ref.namespace ?? "",
    labelSelector ?? "",
    fieldSelector ?? "",
  ] as const
}

function k8sGetKey(ref: ResourceRef & { name: string }) {
  return [
    "k8s",
    ref.apiGroup,
    ref.apiVersion,
    ref.plural,
    ref.namespace ?? "",
    ref.name,
  ] as const
}
