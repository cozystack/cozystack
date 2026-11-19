import { useState, useRef, useEffect, type ReactNode } from "react"
import { ChevronDown } from "lucide-react"

interface Option {
  value: string
  label: string | ReactNode
  disabled?: boolean
}

interface DropdownProps {
  value: string
  onChange: (value: string) => void
  options: Option[]
  placeholder?: string
  className?: string
  disabled?: boolean
  size?: "default" | "sm"
  /** Show a search input. Defaults to true when options > 20. */
  searchable?: boolean
}

export function Dropdown({
  value,
  onChange,
  options,
  placeholder = "Select...",
  className = "",
  disabled,
  size = "default",
  searchable,
}: DropdownProps) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState("")
  const ref = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const showSearch = searchable ?? options.length > 20

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener("mousedown", handler)
    return () => document.removeEventListener("mousedown", handler)
  }, [open])

  useEffect(() => {
    if (open && showSearch) searchRef.current?.focus()
    if (!open) setSearch("")
  }, [open, showSearch])

  const selected = options.find((o) => o.value === value)
  const filtered = search
    ? options.filter(
        (o) =>
          String(o.label).toLowerCase().includes(search.toLowerCase()) ||
          o.value.toLowerCase().includes(search.toLowerCase()),
      )
    : options

  const triggerSizeClass =
    size === "sm"
      ? "gap-1 rounded-md border-slate-200 px-2 py-0.5 text-sm font-medium text-slate-900"
      : "gap-2 rounded-lg border-slate-300 px-3 py-2 text-sm"

  return (
    <div className={`relative ${className}`} ref={ref}>
      <button
        type="button"
        disabled={disabled}
        onClick={() => setOpen(!open)}
        className={`flex w-full items-center justify-between border bg-white text-left outline-none transition-colors hover:bg-slate-50 focus:border-blue-400 ${triggerSizeClass} ${
          disabled ? "bg-slate-50 text-slate-400" : ""
        }`}
      >
        <span className={selected ? "" : "text-slate-400"}>
          {selected ? selected.label : placeholder}
        </span>
        <ChevronDown
          className={`shrink-0 text-slate-400 transition-transform ${
            open ? "rotate-180" : ""
          } ${size === "sm" ? "h-3 w-3" : "h-4 w-4"}`}
        />
      </button>
      {open && (
        <div className="absolute z-30 mt-1 w-full min-w-[160px] rounded-lg border border-slate-200 bg-white shadow-lg">
          {showSearch && (
            <div className="border-b border-slate-100 px-3 py-2">
              <input
                ref={searchRef}
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search..."
                className="w-full text-sm outline-none placeholder:text-slate-400"
              />
            </div>
          )}
          <div className="max-h-60 overflow-auto py-1">
            {filtered.map((opt) => (
              <button
                key={opt.value}
                type="button"
                disabled={opt.disabled}
                onClick={() => {
                  onChange(opt.value)
                  setOpen(false)
                }}
                className={`flex w-full items-center px-3 py-1.5 text-left text-sm transition-colors ${
                  opt.value === value
                    ? "bg-blue-50 font-medium text-blue-700"
                    : "text-slate-700 hover:bg-slate-50"
                } ${opt.disabled ? "cursor-not-allowed opacity-40" : ""}`}
              >
                {opt.label}
              </button>
            ))}
            {filtered.length === 0 && (
              <div className="px-3 py-2 text-sm text-slate-400">No results</div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
