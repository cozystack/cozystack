import { useEffect, useState } from "react"
import { useK8sList } from "@cozystack/k8s-client"
import type { K8sResource } from "@cozystack/k8s-client"
import { parseQuantity, humanizeBytes, humanizeCpu } from "../lib/k8s-quantity.ts"

interface ResourceQuotaSpec {
  hard?: Record<string, string>
}

interface ResourceQuotaStatus {
  hard?: Record<string, string>
  used?: Record<string, string>
}

export interface ResourceQuota extends K8sResource<ResourceQuotaSpec, ResourceQuotaStatus> {
  kind: "ResourceQuota"
}

export interface QuotaEntry {
  label: string
  usedRaw: string
  hardRaw: string
  usedNum: number
  hardNum: number
  /** Capped at 100 for visual gauge rendering */
  pct: number
  /** Real percentage, may exceed 100 when over-limit */
  pctReal: number
  display: string
}

const DISPLAY_KEYS: Array<{
  key: string
  label: string
  format: (n: number) => string
}> = [
  { key: "limits.cpu", label: "CPU", format: humanizeCpu },
  { key: "limits.memory", label: "Memory", format: humanizeBytes },
  { key: "requests.storage", label: "Storage", format: humanizeBytes },
  { key: "services.loadbalancers", label: "Load Balancers", format: (n) => String(n) },
  { key: "persistentvolumeclaims", label: "PVCs", format: (n) => String(n) },
]

function buildEntries(hard: Record<string, string>, used: Record<string, string>): QuotaEntry[] {
  const entries: QuotaEntry[] = []
  for (const { key, label, format } of DISPLAY_KEYS) {
    const hardRaw = hard[key]
    if (!hardRaw) continue
    const usedRaw = used[key] ?? "0"
    const hardNum = parseQuantity(hardRaw)
    const usedNum = parseQuantity(usedRaw)
    if (hardNum <= 0) continue
    const pctReal = (usedNum / hardNum) * 100
    const pct = Math.min(100, pctReal)
    entries.push({ label, usedRaw, hardRaw, usedNum, hardNum, pct, pctReal, display: `${format(usedNum)} / ${format(hardNum)}` })
  }
  return entries
}

/**
 * Aggregates multiple ResourceQuota objects for the same namespace following
 * Kubernetes semantics: the most restrictive hard limit wins (min), and used
 * is the maximum across all quotas that bound a given key.
 *
 * QuotaDisplay reads canonical Kubernetes keys (requests.cpu, limits.memory,
 * etc.) while the cozystack-tenants chart editor uses short keys (cpu, memory)
 * that cozy-lib.resources.flatten translates downstream — the asymmetry is
 * intentional; do not "align" them.
 */
function aggregateQuotas(quotas: ResourceQuota[]): { hard: Record<string, string>; used: Record<string, string> } {
  const hard: Record<string, string> = {}
  const used: Record<string, string> = {}

  for (const q of quotas) {
    const qHard = q.status?.hard ?? {}
    const qUsed = q.status?.used ?? {}

    for (const [key, val] of Object.entries(qHard)) {
      if (!(key in hard) || parseQuantity(val) < parseQuantity(hard[key])) {
        hard[key] = val
      }
    }

    // Only track used for keys bounded by this specific quota object
    for (const [key, val] of Object.entries(qUsed)) {
      if (key in qHard) {
        if (!(key in used) || parseQuantity(val) > parseQuantity(used[key])) {
          used[key] = val
        }
      }
    }
  }

  return { hard, used }
}

function gaugeStrokeColor(pct: number): string {
  if (pct >= 90) return "#ef4444"
  if (pct >= 80) return "#f59e0b"
  return "#3b82f6"
}

function gaugeTrackColor(pct: number): string {
  if (pct >= 90) return "#fee2e2"
  if (pct >= 80) return "#fef3c7"
  return "#dbeafe"
}

function gaugeBgClass(pct: number): string {
  if (pct >= 90) return "bg-red-50 ring-1 ring-red-100"
  if (pct >= 80) return "bg-amber-50 ring-1 ring-amber-100"
  return "bg-slate-50 ring-1 ring-slate-100"
}

interface ArcGaugeProps {
  pct: number
  size?: number
  strokeWidth?: number
  delay?: number
}

function ArcGauge({ pct, size = 72, strokeWidth = 6, delay = 0 }: ArcGaugeProps) {
  const [drawn, setDrawn] = useState(false)

  useEffect(() => {
    const t = setTimeout(() => setDrawn(true), 60 + delay)
    return () => clearTimeout(t)
  }, [delay])

  const r = (size - strokeWidth) / 2
  const circumference = 2 * Math.PI * r
  const dashoffset = drawn ? circumference * (1 - Math.min(pct, 100) / 100) : circumference
  const stroke = gaugeStrokeColor(pct)
  const track = gaugeTrackColor(pct)

  return (
    <svg
      width={size}
      height={size}
      style={{ transform: "rotate(-90deg)" }}
      aria-hidden="true"
    >
      <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke={track} strokeWidth={strokeWidth} />
      <circle
        cx={size / 2}
        cy={size / 2}
        r={r}
        fill="none"
        stroke={stroke}
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeDasharray={circumference}
        strokeDashoffset={dashoffset}
        style={{ transition: `stroke-dashoffset 0.85s cubic-bezier(0.4, 0, 0.2, 1) ${delay}ms` }}
      />
    </svg>
  )
}

interface GaugeCardProps {
  entry: QuotaEntry
  index: number
}

export function GaugeCard({ entry, index }: GaugeCardProps) {
  const stroke = gaugeStrokeColor(entry.pct)
  const isOver = entry.usedNum > entry.hardNum

  return (
    <div
      className={`relative flex flex-col items-center gap-1.5 rounded-xl px-3 py-3 ${gaugeBgClass(entry.pct)}`}
      role="progressbar"
      aria-valuenow={Math.round(entry.pctReal)}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={`${entry.label}: ${entry.display}`}
    >
      <div className="relative flex items-center justify-center">
        <ArcGauge pct={entry.pct} size={72} strokeWidth={6} delay={index * 80} />
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="font-mono text-[13px] font-semibold leading-none" style={{ color: stroke }}>
            {Math.round(entry.pctReal)}%
          </span>
        </div>
        {isOver && (
          <span className="absolute -right-1 -top-1 flex size-4 items-center justify-center rounded-full bg-red-500 text-[9px] font-bold text-white leading-none">
            !
          </span>
        )}
      </div>
      <div className="text-center">
        <div className="text-[10px] font-semibold uppercase tracking-wider text-slate-500">{entry.label}</div>
        <div className="mt-0.5 font-mono text-[10px] text-slate-400">{entry.display}</div>
      </div>
    </div>
  )
}

interface QuotaBarsProps {
  hard: Record<string, string>
  used: Record<string, string>
  compact?: boolean
}

export function QuotaBars({ hard, used, compact = false }: QuotaBarsProps) {
  const entries = buildEntries(hard, used)
  if (entries.length === 0) return null

  if (compact) {
    return (
      <div className="flex flex-wrap gap-x-3 gap-y-1">
        {entries.map((e) => (
          <div key={e.label} title={`${e.label}: ${e.display}`} className="flex items-center gap-1.5 min-w-0">
            <div className="relative h-1.5 w-14 overflow-hidden rounded-full bg-slate-100">
              <div
                className="absolute inset-y-0 left-0 rounded-full transition-all duration-700"
                style={{ width: `${e.pct}%`, backgroundColor: gaugeStrokeColor(e.pct) }}
              />
            </div>
            <span className="font-mono text-[10px] text-slate-500 shrink-0">{Math.round(e.pctReal)}%</span>
          </div>
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-2.5">
      {entries.map((e) => (
        <div key={e.label} className="flex items-center gap-3">
          <span className="w-24 shrink-0 text-xs font-medium text-slate-600">{e.label}</span>
          <div className="relative flex-1 h-2 overflow-hidden rounded-full bg-slate-100">
            <div
              className="absolute inset-y-0 left-0 rounded-full transition-all duration-700"
              style={{ width: `${e.pct}%`, backgroundColor: gaugeStrokeColor(e.pct) }}
            />
          </div>
          <span className="w-28 text-right font-mono text-xs text-slate-500">{e.display}</span>
        </div>
      ))}
    </div>
  )
}

interface QuotaPanelProps {
  namespace: string
}

export function QuotaPanel({ namespace }: QuotaPanelProps) {
  const { data, isLoading } = useK8sList<ResourceQuota>(
    { apiGroup: "", apiVersion: "v1", plural: "resourcequotas", namespace },
    { enabled: !!namespace }
  )

  if (isLoading) return null

  const quotas = data?.items ?? []
  if (quotas.length === 0) return null

  const { hard, used } = aggregateQuotas(quotas)

  const entries = buildEntries(hard, used)
  if (entries.length === 0) return null

  const hasCritical = entries.some((e) => e.pct >= 90)
  const hasWarning = entries.some((e) => e.pct >= 80)

  return (
    <div className={`rounded-xl border p-4 ${hasCritical ? "border-red-200" : hasWarning ? "border-amber-200" : "border-slate-200"} bg-white`}>
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-xs font-semibold uppercase tracking-wider text-slate-500">Resource Quotas</h3>
        {hasCritical && (
          <span className="rounded-full bg-red-100 px-2 py-0.5 text-[11px] font-semibold text-red-600">Critical</span>
        )}
        {!hasCritical && hasWarning && (
          <span className="rounded-full bg-amber-100 px-2 py-0.5 text-[11px] font-semibold text-amber-600">Warning</span>
        )}
      </div>
      <div
        className="grid gap-3"
        style={{ gridTemplateColumns: `repeat(${Math.min(entries.length, 5)}, minmax(0, 1fr))` }}
      >
        {entries.map((entry, i) => (
          <GaugeCard key={entry.label} entry={entry} index={i} />
        ))}
      </div>
    </div>
  )
}

interface TenantQuotaCompactProps {
  /** Pre-fetched ResourceQuota objects for this tenant's namespace. */
  quotas: ResourceQuota[]
}

export function TenantQuotaCompact({ quotas }: TenantQuotaCompactProps) {
  if (!quotas.length) return <span className="text-xs text-slate-400">—</span>

  const { hard, used } = aggregateQuotas(quotas)

  const entries = buildEntries(hard, used)
  if (entries.length === 0) return <span className="text-xs text-slate-400">—</span>

  return <QuotaBars hard={hard} used={used} compact />
}
