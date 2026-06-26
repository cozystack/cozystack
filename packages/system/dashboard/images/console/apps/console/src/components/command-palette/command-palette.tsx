import { useState, useCallback, useRef, useEffect, forwardRef } from "react"
import { useLocation } from "react-router"
import { Search, ChevronRight, ArrowLeft } from "lucide-react"
import {
  DialogRoot,
  DialogPortal,
  DialogBackdrop,
  DialogPopup,
} from "../ui/dialog"
import { cn } from "@cozystack/ui"
import { useIsMac } from "../../hooks/use-is-mac"
import { useKeyboardNav } from "./use-keyboard-nav"
import { useCommandItems } from "./use-command-items"
import { useCommandPalette } from "./command-palette-provider"
import type { NavigationLevel } from "./types"

export function CommandPalette() {
  const { open, setOpen } = useCommandPalette()
  const [query, setQuery] = useState("")
  const [level, setLevel] = useState<NavigationLevel>({ type: "root" })
  const inputRef = useRef<HTMLInputElement>(null)
  const location = useLocation()
  const isMac = useIsMac()

  // Reset local state when palette closes
  useEffect(() => {
    if (!open) {
      setQuery("")
      setLevel({ type: "root" })
    }
  }, [open])

  const close = useCallback(() => {
    setOpen(false)
  }, [setOpen])

  // Close on route change
  useEffect(() => {
    close()
  }, [location.pathname, close])

  const navigate = useCallback((next: NavigationLevel) => {
    setLevel(next)
    setQuery("")
  }, [])

  const goBack = useCallback(() => {
    if (level.type === "instance") {
      setLevel({
        type: "resource",
        plural: level.plural,
        label: level.resourceLabel,
        icon: level.icon,
      })
    } else {
      setLevel({ type: "root" })
    }
    setQuery("")
  }, [level])

  const { items, isLoading } = useCommandItems(query, level, navigate, close)
  const { highlightedIndex, onKeyDown: navKeyDown, setItemRef } =
    useKeyboardNav(items)

  const handleOpenChange = useCallback(
    (nextOpen: boolean) => {
      setOpen(nextOpen)
    },
    [setOpen]
  )

  const handleSelect = useCallback(
    (index: number) => {
      items[index]?.onSelect()
    },
    [items]
  )

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      // Backspace on empty input → go back
      if (e.key === "Backspace" && query === "" && level.type !== "root") {
        e.preventDefault()
        goBack()
        return
      }
      navKeyDown(e)
    },
    [query, level.type, goBack, navKeyDown]
  )

  useEffect(() => {
    if (open) {
      requestAnimationFrame(() => inputRef.current?.focus())
    }
  }, [open])

  // Group items by group label (preserve order)
  const grouped: { label: string; items: { item: (typeof items)[0]; globalIndex: number }[] }[] = []
  const groupMap = new Map<string, number>()
  for (let i = 0; i < items.length; i++) {
    const item = items[i]
    const groupLabel = item.group ?? ""
    let idx = groupMap.get(groupLabel)
    if (idx === undefined) {
      idx = grouped.length
      groupMap.set(groupLabel, idx)
      grouped.push({ label: groupLabel, items: [] })
    }
    grouped[idx].items.push({ item, globalIndex: i })
  }

  const breadcrumb =
    level.type === "resource"
      ? level.label
      : level.type === "instance"
        ? level.label
        : null

  return (
    <DialogRoot open={open} onOpenChange={handleOpenChange} modal>
      <DialogPortal>
        <DialogBackdrop />
        <DialogPopup
          className="w-full max-w-[640px] overflow-hidden p-0"
          aria-label="Command palette"
        >
          {/* Header with optional back + search */}
          <div className="flex items-center gap-2 border-b border-slate-100 px-4" onKeyDown={handleKeyDown}>
            {breadcrumb ? (
              <button
                type="button"
                onClick={goBack}
                className="flex items-center gap-1 shrink-0 rounded-md px-2 py-1 text-xs font-medium text-slate-500 bg-slate-100 hover:bg-slate-200 hover:text-slate-900 transition-colors"
              >
                <ArrowLeft className="h-3 w-3" />
                {breadcrumb}
              </button>
            ) : (
              <Search className="h-4 w-4 shrink-0 text-slate-400" />
            )}
            <input
              ref={inputRef}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={
                breadcrumb
                  ? `Search in ${breadcrumb}...`
                  : "Type a command or search..."
              }
              className="h-12 w-full bg-transparent text-sm text-slate-900 outline-none placeholder:text-slate-400"
            />
            {!breadcrumb && (
              <kbd className="pointer-events-none ml-1 hidden shrink-0 select-none items-center rounded-md border border-slate-200 bg-slate-50 px-1.5 py-0.5 font-mono text-[10px] font-medium text-slate-500 sm:flex">
                {isMac ? "⌘K" : "Ctrl+K"}
              </kbd>
            )}
          </div>

          {/* Results list */}
          <div
            className="max-h-[320px] overflow-y-auto p-1.5"
            role="listbox"
            aria-label="Results"
          >
            {items.length === 0 && !isLoading && (
              <div className="py-8 text-center text-sm text-slate-400">
                No results found.
              </div>
            )}

            {isLoading && items.length === 0 && (
              <div className="py-8 text-center text-sm text-slate-400">
                Loading…
              </div>
            )}

            {grouped.map((group) => (
              <div key={group.label} role="group" aria-label={group.label}>
                {group.label && (
                  <div className="px-2 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-widest text-slate-400">
                    {group.label}
                  </div>
                )}
                {group.items.map(({ item, globalIndex }) => (
                  <PaletteItem
                    key={item.id}
                    ref={(el) => setItemRef(globalIndex, el)}
                    highlighted={globalIndex === highlightedIndex}
                    icon={item.icon}
                    label={item.label}
                    description={item.description}
                    drilldown={item.drilldown}
                    onSelect={() => handleSelect(globalIndex)}
                  />
                ))}
              </div>
            ))}
          </div>
        </DialogPopup>
      </DialogPortal>
    </DialogRoot>
  )
}

const PaletteItem = forwardRef<
  HTMLDivElement,
  {
    highlighted: boolean
    icon?: React.ReactNode
    label: string
    description?: string
    drilldown?: boolean
    onSelect: () => void
  }
>(function PaletteItem(
  { highlighted, icon, label, description, drilldown, onSelect },
  ref
) {
  return (
    <div
      ref={ref}
      role="option"
      aria-selected={highlighted}
      className={cn(
        "relative flex cursor-default items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm outline-none select-none transition-colors",
        highlighted ? "bg-blue-50 text-blue-900" : "text-slate-700 hover:bg-slate-50"
      )}
      onClick={onSelect}
    >
      {icon && (
        <span className="flex h-5 w-5 items-center justify-center shrink-0">
          {icon}
        </span>
      )}
      <span className="flex-1 truncate font-medium">{label}</span>
      {description && (
        <span className={cn("text-xs truncate max-w-[180px]", highlighted ? "text-blue-500" : "text-slate-400")}>
          {description}
        </span>
      )}
      {drilldown && (
        <ChevronRight className={cn("h-3.5 w-3.5 shrink-0", highlighted ? "text-blue-400" : "text-slate-300")} />
      )}
    </div>
  )
})
