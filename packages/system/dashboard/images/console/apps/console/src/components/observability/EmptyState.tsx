import type { ReactNode } from "react"
import { cn, Spinner } from "@cozystack/ui"

interface EmptyStateProps {
  title: ReactNode
  hint?: ReactNode
  busy?: boolean
  tone?: "light" | "dark"
  className?: string
}

export function EmptyState({ title, hint, busy, tone = "light", className }: EmptyStateProps) {
  const dark = tone === "dark"
  return (
    <div
      role="status"
      aria-busy={busy || undefined}
      className={cn("flex h-full flex-col items-center justify-center gap-1.5 px-4 py-8 text-center", className)}
    >
      {busy && <Spinner className={cn("mb-1 size-5", dark && "text-slate-500")} />}
      <p className={cn("text-sm", dark ? "text-slate-300" : "text-slate-600")}>{title}</p>
      {hint && <p className={cn("text-xs", dark ? "text-slate-500" : "text-slate-400")}>{hint}</p>}
    </div>
  )
}
