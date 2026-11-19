import { Link } from "react-router"
import type { ApplicationDefinition } from "@cozystack/types"
import {
  appDisplayName,
  iconDataUrl,
} from "../lib/app-definitions.ts"

interface AppCardProps {
  ad: ApplicationDefinition
  to: string
}

export function AppCard({ ad, to }: AppCardProps) {
  const icon = iconDataUrl(ad)
  const name = appDisplayName(ad)
  const description = ad.spec?.dashboard?.description ?? ""
  return (
    <Link
      to={to}
      className="group flex w-80 items-start gap-3 rounded-lg border border-slate-200 bg-white p-4 transition-shadow hover:shadow-sm"
    >
      <div className="size-11 shrink-0 overflow-hidden rounded-md bg-slate-100">
        {icon ? (
          <img src={icon} alt={name} className="h-full w-full" />
        ) : (
          <div className="flex h-full w-full items-center justify-center text-sm font-semibold text-slate-400">
            {name.slice(0, 2).toUpperCase()}
          </div>
        )}
      </div>
      <div className="min-w-0 flex-1">
        <h3 className="truncate text-sm font-semibold text-slate-900 group-hover:text-blue-700">
          {name}
        </h3>
        <p className="mt-0.5 line-clamp-2 text-xs text-slate-500">{description}</p>
      </div>
    </Link>
  )
}
