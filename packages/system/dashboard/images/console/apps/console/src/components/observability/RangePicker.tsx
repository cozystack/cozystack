import { cn } from "@cozystack/ui"

export function RangePicker<K extends string>({
  options,
  value,
  onChange,
}: {
  options: readonly K[]
  value: K
  onChange: (value: K) => void
}) {
  return (
    <div role="group" aria-label="Time range" className="inline-flex h-7 items-center rounded-lg border border-slate-300 bg-white p-0.5 shadow-xs">
      {options.map((key) => (
        <button
          key={key}
          type="button"
          aria-pressed={value === key}
          onClick={() => onChange(key)}
          className={cn(
            "h-full rounded-md px-2.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-slate-400 focus-visible:ring-offset-2",
            value === key ? "bg-slate-900 text-white" : "text-slate-600 hover:bg-slate-100",
          )}
        >
          {key}
        </button>
      ))}
    </div>
  )
}
