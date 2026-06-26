import type { ReactNode } from "react"
import { cn } from "../lib/utils.ts"

interface SectionProps {
  title?: ReactNode
  description?: ReactNode
  actions?: ReactNode
  className?: string
  bodyClassName?: string
  children?: ReactNode
}

/**
 * A padded, bordered panel used for grouping content on detail/create pages.
 * Matches the Cozystack design language: white card, slate-200 border,
 * rounded-lg, generous padding.
 */
export function Section({
  title,
  description,
  actions,
  className,
  bodyClassName,
  children,
}: SectionProps) {
  return (
    <section className={cn("rounded-lg border border-slate-200 bg-white shadow-xs", className)}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-slate-200 px-5 py-3">
          <div>
            {title && (
              <h2 className="text-base font-semibold text-slate-900">{title}</h2>
            )}
            {description && (
              <p className="mt-0.5 text-xs text-slate-500">{description}</p>
            )}
          </div>
          {actions && <div className="shrink-0">{actions}</div>}
        </header>
      )}
      <div className={cn("p-5", bodyClassName)}>{children}</div>
    </section>
  )
}
