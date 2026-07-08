import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import { Section, Spinner, StatusBadge } from "@cozystack/ui"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { appInstanceLabel } from "../../lib/labels.ts"
import { formatAge } from "../../lib/status.ts"

interface WorkloadStatus {
  replicas?: number
  readyReplicas?: number
  availableReplicas?: number
}

interface PodStatus {
  phase?: string
  containerStatuses?: { ready: boolean; restartCount: number }[]
}

function WorkloadList({
  title,
  ref,
  label,
}: {
  title: string
  ref: { apiGroup: string; apiVersion: string; plural: string; namespace: string }
  label: string
}) {
  const { data, isLoading } = useK8sList<K8sResource<unknown, WorkloadStatus>>(ref, {
    labelSelector: label,
  })
  const items = data?.items ?? []
  if (isLoading) {
    return (
      <Section title={title} bodyClassName="p-0">
        <div className="flex items-center gap-2 px-5 py-4 text-xs text-slate-500">
          <Spinner /> Loading…
        </div>
      </Section>
    )
  }
  if (items.length === 0) return null

  return (
    <Section title={title} bodyClassName="p-0">
      <ul className="divide-y divide-slate-100">
        {items.map((item) => {
          const status = item.status
          const ready = status?.readyReplicas ?? 0
          const desired = status?.replicas ?? 0
          return (
            <li
              key={item.metadata.name}
              className="flex items-center justify-between px-5 py-2.5 text-sm"
            >
              <span className="font-mono text-xs text-slate-800">
                {item.metadata.name}
              </span>
              <span className="flex items-center gap-3 text-xs text-slate-500 tabular-nums">
                <span>
                  {ready}/{desired}
                </span>
                <span>{formatAge(item.metadata.creationTimestamp)}</span>
              </span>
            </li>
          )
        })}
      </ul>
    </Section>
  )
}

function PodsList({ namespace, label }: { namespace: string; label: string }) {
  const { data, isLoading } = useK8sList<K8sResource<unknown, PodStatus>>(
    { apiGroup: "", apiVersion: "v1", plural: "pods", namespace },
    { labelSelector: label },
  )
  const items = data?.items ?? []
  if (isLoading) {
    return (
      <Section title="Pods" bodyClassName="p-0">
        <div className="flex items-center gap-2 px-5 py-4 text-xs text-slate-500">
          <Spinner /> Loading…
        </div>
      </Section>
    )
  }
  if (items.length === 0) return null

  return (
    <Section title="Pods" bodyClassName="p-0">
      <ul className="divide-y divide-slate-100">
        {items.map((pod) => {
          const ready =
            pod.status?.containerStatuses?.filter((c) => c.ready).length ?? 0
          const total = pod.status?.containerStatuses?.length ?? 0
          const phase = pod.status?.phase ?? "Unknown"
          return (
            <li
              key={pod.metadata.name}
              className="flex items-center justify-between px-5 py-2.5 text-sm"
            >
              <span className="font-mono text-xs text-slate-800">
                {pod.metadata.name}
              </span>
              <span className="flex items-center gap-3 text-xs text-slate-500 tabular-nums">
                <StatusBadge tone={phase === "Running" ? "ok" : "warn"}>
                  {phase}
                </StatusBadge>
                <span>
                  {ready}/{total}
                </span>
                <span>{formatAge(pod.metadata.creationTimestamp)}</span>
              </span>
            </li>
          )
        })}
      </ul>
    </Section>
  )
}

export function WorkloadsTab({
  ad,
  instance,
}: {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}) {
  const ns = instance.metadata.namespace ?? ""
  const label = appInstanceLabel(ad, instance)

  return (
    <div className="space-y-4 p-6">
      <WorkloadList
        title="Deployments"
        label={label}
        ref={{ apiGroup: "apps", apiVersion: "v1", plural: "deployments", namespace: ns }}
      />
      <WorkloadList
        title="StatefulSets"
        label={label}
        ref={{ apiGroup: "apps", apiVersion: "v1", plural: "statefulsets", namespace: ns }}
      />
      <WorkloadList
        title="DaemonSets"
        label={label}
        ref={{ apiGroup: "apps", apiVersion: "v1", plural: "daemonsets", namespace: ns }}
      />
      <PodsList namespace={ns} label={label} />
    </div>
  )
}
