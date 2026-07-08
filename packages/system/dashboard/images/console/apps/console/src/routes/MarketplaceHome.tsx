import { useMemo } from "react"
import { Link } from "react-router"
import {
  ArrowRight,
  Cloud,
  Database,
  LayoutGrid,
  Network,
  type LucideIcon,
} from "lucide-react"
import { Button, Spinner } from "@cozystack/ui"
import { groupByCategory, useApplicationDefinitions } from "../lib/app-definitions.ts"

interface CategoryMeta {
  key: string
  title: string
  blurb: string
  icon: LucideIcon
  iconGradient: string
  iconColor: string
}

const CATEGORIES: CategoryMeta[] = [
  {
    key: "IaaS",
    title: "Infrastructure",
    blurb: "Virtual machines, disks, buckets, private networks and Kubernetes clusters.",
    icon: Cloud,
    iconGradient: "from-blue-500 to-cyan-400",
    iconColor: "text-white",
  },
  {
    key: "PaaS",
    title: "Managed services",
    blurb: "Databases, caches, message brokers and other managed platform services.",
    icon: Database,
    iconGradient: "from-violet-500 to-purple-400",
    iconColor: "text-white",
  },
  {
    key: "NaaS",
    title: "Networking",
    blurb: "Load balancers, caches and VPN gateways exposed to your users.",
    icon: Network,
    iconGradient: "from-emerald-500 to-teal-400",
    iconColor: "text-white",
  },
]

/**
 * Landing page of the marketplace: a compact card picker instead of a flat
 * list of every application. Each card reflects how many applications are
 * available in that category and routes to the filtered category view.
 */
export function MarketplaceHome() {
  const { data, isLoading } = useApplicationDefinitions()
  const grouped = useMemo(() => groupByCategory(data), [data])
  const countFor = (category: string) =>
    grouped.find((g) => g.category === category)?.items.length ?? 0

  return (
    <div className="p-6">
      <div className="mb-6 flex items-end justify-between gap-4 animate-[fade-up_0.3s_ease_both]">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Marketplace</h1>
          <p className="mt-0.5 text-sm text-slate-500">Choose a product to deploy.</p>
        </div>
        <Link to="/marketplace/all">
          <Button variant="outline" size="sm">
            <LayoutGrid className="size-3.5" /> Show all apps
          </Button>
        </Link>
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : (
        <div className="flex flex-wrap gap-4">
          {CATEGORIES.map(({ key, title, blurb, icon: Icon, iconGradient, iconColor }, index) => {
            const count = countFor(key)
            const disabled = count === 0
            const card = (
              <div
                className="flex h-full w-80 flex-col gap-3 rounded-xl border border-slate-200 bg-white p-5 shadow-xs transition-all duration-200 group-hover:-translate-y-0.5 group-hover:shadow-md group-hover:border-slate-300"
                style={{ animationDelay: `${index * 60}ms` }}
              >
                <div
                  className={`flex size-10 items-center justify-center rounded-lg bg-gradient-to-br ${iconGradient} shadow-sm`}
                >
                  <Icon className={`size-5 ${iconColor}`} />
                </div>
                <div>
                  <p className="text-xs font-semibold uppercase tracking-wider text-slate-400">
                    {key}
                  </p>
                  <h3 className="mt-0.5 text-base font-semibold text-slate-900">
                    {title}
                  </h3>
                </div>
                <p className="flex-1 text-sm text-slate-500">{blurb}</p>
                <div className="mt-1 text-sm">
                  {disabled ? (
                    <span className="text-slate-400">Coming soon</span>
                  ) : (
                    <span className="inline-flex items-center gap-1 font-medium text-blue-600 transition-gap duration-150 group-hover:gap-1.5">
                      Browse {count} {count === 1 ? "product" : "products"}
                      <ArrowRight className="size-3.5 transition-transform duration-150 group-hover:translate-x-0.5" />
                    </span>
                  )}
                </div>
              </div>
            )
            return disabled ? (
              <div
                key={key}
                className="opacity-50 animate-[fade-up_0.4s_ease_both]"
                style={{ animationDelay: `${index * 60}ms` }}
              >
                {card}
              </div>
            ) : (
              <Link
                key={key}
                to={`/marketplace/c/${encodeURIComponent(key)}`}
                className="group block animate-[fade-up_0.4s_ease_both]"
                style={{ animationDelay: `${index * 60}ms` }}
              >
                {card}
              </Link>
            )
          })}
        </div>
      )}
    </div>
  )
}
