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
    <div className="inline-flex rounded-md border border-slate-200 bg-white p-0.5">
      {options.map((key) => (
        <button
          key={key}
          type="button"
          aria-pressed={value === key}
          onClick={() => onChange(key)}
          className={cn(
            "rounded px-2.5 py-1 text-xs",
            value === key ? "bg-slate-900 text-white" : "text-slate-600 hover:bg-slate-100",
          )}
        >
          {key}
        </button>
      ))}
    </div>
  )
}
