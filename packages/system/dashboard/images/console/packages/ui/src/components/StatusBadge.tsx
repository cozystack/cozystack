import { cn } from "../lib/utils.ts"

type Tone = "ok" | "warn" | "error" | "muted" | "info"

interface StatusBadgeProps {
  tone?: Tone
  children: React.ReactNode
  className?: string
}

const toneClasses: Record<Tone, string> = {
  ok: "bg-emerald-50 text-emerald-700 ring-1 ring-emerald-200",
  warn: "bg-amber-50 text-amber-700 ring-1 ring-amber-200",
  error: "bg-red-50 text-red-700 ring-1 ring-red-200",
  muted: "bg-slate-100 text-slate-500",
  info: "bg-blue-50 text-blue-700 ring-1 ring-blue-200",
}

const dotClasses: Record<Tone, string> = {
  ok: "bg-emerald-500",
  warn: "bg-amber-500",
  error: "bg-red-500",
  muted: "bg-slate-400",
  info: "bg-blue-500",
}

export function StatusBadge({ tone = "muted", children, className }: StatusBadgeProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium",
        toneClasses[tone],
        className,
      )}
    >
      <span className={cn("size-1.5 shrink-0 rounded-full", dotClasses[tone])} />
      {children}
    </span>
  )
}
