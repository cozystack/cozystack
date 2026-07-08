import { Link } from "react-router"
import { StatusBadge } from "@cozystack/ui"
import type {
  ApplicationDefinition,
  ApplicationInstance,
} from "@cozystack/types"
import { appDisplayName, iconDataUrl } from "../lib/app-definitions.ts"
import { formatAge, readyCondition } from "../lib/status.ts"

interface InstanceCardProps {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}

export function InstanceCard({ ad, instance }: InstanceCardProps) {
  const icon = iconDataUrl(ad)
  const displayKind = appDisplayName(ad)
  const ready = readyCondition(instance)
  const plural = ad.spec?.application.plural ?? ""

  return (
    <Link
      to={`/console/${plural}/${instance.metadata.name}`}
      className="group flex w-80 items-center justify-between gap-3 rounded-lg border border-slate-200 bg-white px-4 py-3 transition-shadow hover:shadow-sm"
    >
      <div className="flex min-w-0 items-center gap-3">
        <div className="size-9 shrink-0 overflow-hidden rounded-md bg-slate-100">
          {icon ? (
            <img src={icon} alt={displayKind} className="h-full w-full" />
          ) : null}
        </div>
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold text-slate-900 group-hover:text-blue-700">
            {instance.metadata.name}
          </p>
          <p className="truncate text-xs text-slate-500">{displayKind}</p>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-3 text-xs text-slate-500">
        {ready ? (
          <StatusBadge tone={ready.status === "True" ? "ok" : "warn"}>
            {ready.status === "True" ? "Ready" : (ready.reason ?? "NotReady")}
          </StatusBadge>
        ) : (
          <StatusBadge tone="muted">Unknown</StatusBadge>
        )}
        <span className="tabular-nums">{formatAge(instance.metadata.creationTimestamp)}</span>
      </div>
    </Link>
  )
}
