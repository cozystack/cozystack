import { NavLink } from "react-router"
import { cn } from "@cozystack/ui"

export interface Tab {
  to: string
  label: string
  end?: boolean
}

export function TabBar({ tabs }: { tabs: Tab[] }) {
  return (
    <div className="flex gap-0.5">
      {tabs.map((tab) => (
        <NavLink
          key={tab.to}
          to={tab.to}
          end={tab.end ?? false}
          className={({ isActive }) =>
            cn(
              "relative border-b-2 px-3 py-2 text-sm transition-all duration-150",
              isActive
                ? "border-blue-600 font-medium text-blue-700"
                : "border-transparent text-slate-500 hover:text-slate-800 hover:border-slate-300",
            )
          }
        >
          {tab.label}
        </NavLink>
      ))}
    </div>
  )
}
