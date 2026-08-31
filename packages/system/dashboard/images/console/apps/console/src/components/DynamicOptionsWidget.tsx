import { useEffect, useRef } from "react"
import type { WidgetProps } from "@rjsf/utils"
import { useK8sGet, useK8sList } from "@cozystack/k8s-client"
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

/**
 * Resolves `{path.to.field}` placeholders in an option source against the whole
 * form, so a dropdown can be scoped by a choice made elsewhere in it.
 *
 * Returns null when a placeholder has no value yet — the field it depends on is
 * still empty, and asking the server for `vmimportvm.` would only 404.
 */
export function resolveSource(
  source: string | undefined,
  root: unknown,
): { name: string | null; parameterised: boolean } {
  if (!source) return { name: null, parameterised: false }
  if (!source.includes("{")) return { name: source, parameterised: false }

  let missing = false
  const name = source.replace(/\{([^}]+)\}/g, (_, path: string) => {
    const resolved = path
      .split(".")
      .reduce<unknown>(
        (acc, key) =>
          acc && typeof acc === "object"
            ? (acc as Record<string, unknown>)[key]
            : undefined,
        root,
      )
    if (typeof resolved !== "string" || resolved === "") {
      missing = true
      return ""
    }
    return resolved
  })
  return { name: missing ? null : name, parameterised: true }
}

export function DynamicOptionsWidget(props: WidgetProps) {
  const { value, onChange, required, disabled, readonly, schema, registry } = props
  const { tenantNamespace } = useTenantContext()

  const rawSource = (schema as { "x-cozystack-options"?: { source?: string } })?.[
    "x-cozystack-options"
  ]?.source

  const rootFormData = (registry?.formContext as { rootFormData?: unknown })?.rootFormData
  const { name: source, parameterised } = resolveSource(rawSource, rootFormData)

  // A parameterised source is deliberately absent from the Option list — it
  // describes nothing without its argument — so it is fetched by name instead.
  const { data: optionList, isLoading: listLoading } = useK8sList<OptionObject>(
    {
      apiGroup: "core.cozystack.io",
      apiVersion: "v1alpha1",
      plural: "options",
      namespace: tenantNamespace ?? undefined,
    },
    { enabled: !!tenantNamespace && !parameterised },
  )

  const { data: option, isLoading: getLoading } = useK8sGet<OptionObject>(
    {
      apiGroup: "core.cozystack.io",
      apiVersion: "v1alpha1",
      plural: "options",
      name: source ?? "",
      namespace: tenantNamespace ?? "",
    },
    { enabled: !!tenantNamespace && parameterised && !!source },
  )

  const isLoading = parameterised ? getLoading : listLoading

  const items: OptionItem[] = parameterised
    ? (option?.spec?.items ?? [])
    : (optionList?.items?.find((o) => o.metadata.name === source)?.spec?.items ?? [])

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
    : // A dependent dropdown with nothing to depend on yet is waiting, not
      // empty, and saying so points at the field that unblocks it.
      parameterised && !source
      ? "Select a source first..."
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
