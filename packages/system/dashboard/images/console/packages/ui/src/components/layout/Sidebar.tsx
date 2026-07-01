import { useState, type ComponentType, type ReactNode } from "react"
import { NavLink, useLocation } from "react-router"
import { ChevronUp } from "lucide-react"
import { cn } from "../../lib/utils.ts"

export interface SidebarItem {
  label: string
  to: string
  icon?: ComponentType<{ className?: string }>
  badge?: ReactNode
  end?: boolean
}

export interface SidebarSection {
  title: string
  items: SidebarItem[]
}

interface SidebarProps {
  sections: SidebarSection[]
}

export function Sidebar({ sections }: SidebarProps) {
  const location = useLocation()
  const search = location.search
  // Track only explicit overrides. Missing keys default to "open" so sections
  // added after mount (e.g. once ApplicationDefinitions finish loading) are
  // expanded automatically without waiting for a toggle.
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})

  if (sections.length === 0) return null

  const isOpen = (title: string) => !collapsed[title]
  const toggleSection = (title: string) => {
    setCollapsed((prev) => ({ ...prev, [title]: !prev[title] }))
  }

  return (
    <aside className="flex w-52 shrink-0 flex-col border-r border-slate-200 bg-white">
      <nav className="flex-1 overflow-y-auto py-3">
        {sections.map((section) => (
          <div key={section.title} className="mb-1">
            <button
              type="button"
              onClick={() => toggleSection(section.title)}
              className="flex w-full items-center justify-between px-4 py-1.5 text-xs font-semibold uppercase tracking-wider text-slate-400 hover:text-slate-600"
            >
              {section.title}
              <ChevronUp
                className={cn(
                  "h-3.5 w-3.5 transition-transform",
                  !isOpen(section.title) && "rotate-180",
                )}
              />
            </button>
            {isOpen(section.title) && (
              <div className="mt-0.5 space-y-0.5 px-2">
                {section.items.map((item) => (
                  <NavLink
                    key={item.to}
                    to={`${item.to}${search}`}
                    end={item.end ?? false}
                    className={({ isActive }) =>
                      cn(
                        "relative flex w-full items-center gap-2.5 rounded-md px-3 py-1.5 text-[13px] transition-all duration-150",
                        isActive
                          ? "bg-blue-50 font-medium text-blue-700 before:absolute before:left-0 before:top-1/2 before:h-4 before:w-0.5 before:-translate-y-1/2 before:rounded-full before:bg-blue-500 before:content-['']"
                          : "text-slate-600 hover:bg-slate-50 hover:text-slate-900",
                      )
                    }
                  >
                    {({ isActive }) => (
                      <>
                        {item.icon && (
                          <item.icon
                            className={cn(
                              "h-4 w-4 shrink-0 transition-colors",
                              isActive ? "text-blue-500" : "text-slate-400",
                            )}
                          />
                        )}
                        <span className="flex-1 truncate">{item.label}</span>
                        {item.badge != null && (
                          <span className="shrink-0 text-xs text-slate-400">
                            {item.badge}
                          </span>
                        )}
                      </>
                    )}
                  </NavLink>
                ))}
              </div>
            )}
          </div>
        ))}
      </nav>
    </aside>
  )
}
