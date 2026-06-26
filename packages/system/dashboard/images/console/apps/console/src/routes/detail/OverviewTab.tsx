import yaml from "js-yaml"
import { Section, StatusBadge } from "@cozystack/ui"
import type {
  ApplicationDefinition,
  ApplicationInstance,
} from "@cozystack/types"
import { formatAge, readyCondition } from "../../lib/status.ts"

interface OverviewTabProps {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}

export function OverviewTab({ ad: _ad, instance }: OverviewTabProps) {
  const ready = readyCondition(instance)
  return (
    <div className="grid gap-4 p-6 md:grid-cols-3">
      <Section title="Details" className="md:col-span-1">
        <dl className="space-y-2.5 text-sm">
          <div className="flex items-center justify-between">
            <dt className="text-slate-500">Status</dt>
            <dd>
              {ready ? (
                <StatusBadge tone={ready.status === "True" ? "ok" : "warn"}>
                  {ready.status === "True" ? "Ready" : (ready.reason ?? "NotReady")}
                </StatusBadge>
              ) : (
                <StatusBadge tone="muted">Unknown</StatusBadge>
              )}
            </dd>
          </div>
          <div className="flex items-center justify-between">
            <dt className="text-slate-500">Namespace</dt>
            <dd className="font-mono text-xs text-slate-700">
              {instance.metadata.namespace}
            </dd>
          </div>
          <div className="flex items-center justify-between">
            <dt className="text-slate-500">Age</dt>
            <dd className="tabular-nums text-slate-700">
              {formatAge(instance.metadata.creationTimestamp)}
            </dd>
          </div>
          {instance.status?.version && (
            <div className="flex items-center justify-between">
              <dt className="text-slate-500">Version</dt>
              <dd className="font-mono text-xs text-slate-700">
                {instance.status.version}
              </dd>
            </div>
          )}
        </dl>
        {ready?.message && (
          <p className="mt-3 border-t border-slate-100 pt-3 text-xs text-slate-500">
            {ready.message}
          </p>
        )}
      </Section>
      <Section title="Spec" className="md:col-span-2" bodyClassName="p-0">
        <pre className="max-h-[60vh] overflow-auto p-5 text-xs leading-relaxed text-slate-800">
          {yaml.dump(instance.spec ?? {})}
        </pre>
      </Section>
    </div>
  )
}
