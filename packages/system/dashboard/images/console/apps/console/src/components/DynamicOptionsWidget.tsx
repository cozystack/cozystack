import { useEffect, useRef } from "react"
import type { WidgetProps } from "@rjsf/utils"
import { useK8sList } from "@cozystack/k8s-client"
import { useTenantContext } from "../lib/tenant-context.tsx"

/**
 * Generic dropdown widget driven by the `x-cozystack-options` schema keyword.
 *
 * A string field annotated with `{ "x-cozystack-options": { "source": "gpu" } }`
 * is rendered as a <select> populated from the cluster. Options are served by
 * the cozystack-api `Option` resource (core.cozystack.io/v1alpha1): one object
 * per named source, computed server-side with privileged access, so tenants
 * need no direct read on Nodes / KubeVirt / instancetypes / etc.
 *
 * This single widget replaces the former per-field widgets (StorageClass,
 * VMDisk, BackupClass): the server marks the preselected entry via
 * `item.default` (e.g. the default StorageClass) and ships display labels
 * (e.g. disk size), so the UX is identical everywhere.
 */

interface OptionItem {
  value: string
  label?: string
  description?: string
  default?: boolean
}

interface OptionObject {
  apiVersion: string
  kind: string
  metadata: { name: string }
  spec?: { items?: OptionItem[] }
}

export function DynamicOptionsWidget(props: WidgetProps) {
  const { value, onChange, required, disabled, readonly, schema } = props
  const { tenantNamespace } = useTenantContext()

  const source = (schema as { "x-cozystack-options"?: { source?: string } })?.[
    "x-cozystack-options"
  ]?.source

  const { data: optionList, isLoading } = useK8sList<OptionObject>({
    apiGroup: "core.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "options",
    namespace: tenantNamespace ?? undefined,
  })

  const items: OptionItem[] =
    optionList?.items?.find((o) => o.metadata.name === source)?.spec?.items ?? []

  const currentValue = typeof value === "string" ? value : ""
  const hasCurrentInList = items.some((it) => it.value === currentValue)
  const defaultItem = items.find((it) => it.default)

  // Auto-select the server-marked default (e.g. the default StorageClass) once
  // on initial load. The ref latches after the first auto-default and is never
  // reset, so deliberately clearing an optional field sticks — re-arming it
  // here would let the effect immediately re-apply the default and make the
  // field impossible to clear.
  const hasAutoDefaulted = useRef(false)
  useEffect(() => {
    if (!hasAutoDefaulted.current && !value && defaultItem && !isLoading) {
      hasAutoDefaulted.current = true
      onChange(defaultItem.value)
    }
  }, [value, defaultItem, isLoading, onChange])

  const placeholder = isLoading
    ? "Loading..."
    : items.length === 0
      ? "No options available"
      : required
        ? "Select an option..."
        : "-- None --"

  return (
    <select
      value={currentValue}
      onChange={(e) => onChange(e.target.value || undefined)}
      disabled={disabled || readonly}
      required={required}
      className="w-full rounded-lg border border-slate-300 bg-white pl-3 pr-8 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:opacity-50 disabled:cursor-not-allowed"
    >
      {/* Explicit placeholder so a value-less required select shows it instead
          of silently displaying the first option. */}
      <option value="" disabled={required}>
        {placeholder}
      </option>
      {/* Keep the committed value visible even before the list loads, so an
          async re-render never drops the parent's selection. */}
      {currentValue && !hasCurrentInList && (
        <option value={currentValue}>{currentValue}</option>
      )}
      {items.map((it) => (
        <option key={it.value} value={it.value} title={it.description}>
          {it.label || it.value}
        </option>
      ))}
    </select>
  )
}
