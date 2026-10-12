import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { appInstanceLabel } from "../../lib/labels.ts"

export interface ResourcePresence {
  workloads: boolean
  services: boolean
  ingresses: boolean
}

/**
 * Probe which resource groups an application instance actually owns, so the
 * detail page can offer only the tabs that have content — e.g. Info creates
 * secrets but no workloads, services or ingresses. The probes use the exact
 * list refs and label selector of the corresponding tabs, so React Query
 * shares one cache entry (and one watch) per list between probe and tab.
 */
export function useResourcePresence(
  ad: ApplicationDefinition | undefined,
  instance: ApplicationInstance | undefined,
): ResourcePresence {
  const ns = instance?.metadata.namespace ?? ""
  const label = ad && instance ? appInstanceLabel(ad, instance) : ""
  const opts = { labelSelector: label, enabled: !!ns && !!label }

  const deployments = useK8sList<K8sResource>(
    { apiGroup: "apps", apiVersion: "v1", plural: "deployments", namespace: ns },
    opts,
  )
  const statefulsets = useK8sList<K8sResource>(
    { apiGroup: "apps", apiVersion: "v1", plural: "statefulsets", namespace: ns },
    opts,
  )
  const daemonsets = useK8sList<K8sResource>(
    { apiGroup: "apps", apiVersion: "v1", plural: "daemonsets", namespace: ns },
    opts,
  )
  const pods = useK8sList<K8sResource>(
    { apiGroup: "", apiVersion: "v1", plural: "pods", namespace: ns },
    opts,
  )
  const services = useK8sList<K8sResource>(
    { apiGroup: "", apiVersion: "v1", plural: "services", namespace: ns },
    opts,
  )
  const ingresses = useK8sList<K8sResource>(
    { apiGroup: "networking.k8s.io", apiVersion: "v1", plural: "ingresses", namespace: ns },
    opts,
  )

  const has = (q: { data?: { items: unknown[] } }) => (q.data?.items.length ?? 0) > 0

  return {
    workloads: has(deployments) || has(statefulsets) || has(daemonsets) || has(pods),
    services: has(services),
    ingresses: has(ingresses),
  }
}
