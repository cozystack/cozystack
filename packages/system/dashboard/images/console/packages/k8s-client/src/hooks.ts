import {
  useQuery,
  useMutation,
  useQueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query"
import { useEffect, useMemo, useRef, useState } from "react"
import { useK8sClient } from "./provider.tsx"
import type { K8sList, K8sResource } from "./client.ts"

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
  const cleanupRef = useRef<(() => void) | null>(null)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [watchGeneration, setWatchGeneration] = useState(0)

  const query = useQuery<K8sList<T>>({
    queryKey,
    queryFn: () =>
      client.list<T>(ref.apiGroup, ref.apiVersion, ref.plural, ref.namespace, {
        labelSelector,
        fieldSelector,
      }),
    ...queryOptions,
  })

  const resourceVersion = query.data?.metadata?.resourceVersion

  useEffect(() => {
    let active = true
    cleanupRef.current?.()
    cleanupRef.current = null
    if (reconnectTimerRef.current !== null) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = null
    }

    if (!enabled || !watchEnabled || !resourceVersion) return

    const reconnect = () => {
      if (!active || reconnectTimerRef.current !== null) return
      cleanupRef.current?.()
      cleanupRef.current = null
      reconnectTimerRef.current = setTimeout(() => {
        reconnectTimerRef.current = null
        void queryClient.invalidateQueries({ queryKey }).finally(() => {
          // A successful relist may return the same resourceVersion. The
          // generation guarantees that the closed watch still reopens.
          if (active) setWatchGeneration((generation) => generation + 1)
        })
      }, 1000)
    }

    cleanupRef.current = client.watch<T>(
      ref.apiGroup,
      ref.apiVersion,
      ref.plural,
      ref.namespace,
      resourceVersion,
      (event) => {
        if (event.type === "BOOKMARK") return
        if (event.type === "ERROR") {
          reconnect()
          return
        }
        queryClient.setQueryData<K8sList<T>>(queryKey, (old) => {
          if (!old) return old
          const items = [...old.items]
          const idx = items.findIndex(
            (i) =>
              i.metadata.name === event.object.metadata.name &&
              i.metadata.namespace === event.object.metadata.namespace,
          )
          switch (event.type) {
            case "ADDED":
              if (idx === -1) items.push(event.object)
              else items[idx] = event.object
              break
            case "MODIFIED":
              if (idx >= 0) items[idx] = event.object
              else items.push(event.object)
              break
            case "DELETED":
              if (idx >= 0) items.splice(idx, 1)
              break
          }
          return { ...old, items }
        })
      },
      reconnect,
      {
        labelSelector,
        fieldSelector,
      },
    )

    return () => {
      active = false
      if (reconnectTimerRef.current !== null) {
        clearTimeout(reconnectTimerRef.current)
        reconnectTimerRef.current = null
      }
      cleanupRef.current?.()
      cleanupRef.current = null
    }
  }, [
    resourceVersion,
    enabled,
    watchEnabled,
    ref.apiGroup,
    ref.apiVersion,
    ref.plural,
    ref.namespace,
    labelSelector,
    fieldSelector,
    watchGeneration,
    client,
    queryClient,
    queryKey,
  ])

  return query
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
