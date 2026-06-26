import { Check, X } from "lucide-react"
import { Link } from "react-router"
import { Section, Spinner, StatusBadge } from "@cozystack/ui"
import {
  useK8sList,
  type K8sResource,
} from "@cozystack/k8s-client"
import type { ApplicationDefinition } from "@cozystack/types"
import {
  iconDataUrl,
  isTenantModule,
  useApplicationDefinitions,
} from "../lib/app-definitions.ts"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { humanizeKind } from "../lib/humanize.ts"

const TENANT_MODULES_REF = {
  apiGroup: "core.cozystack.io",
  apiVersion: "v1alpha1",
  plural: "tenantmodules",
}

const APP_KIND_LABEL = "apps.cozystack.io/application.kind"

/**
 * Administration → Modules: every ApplicationDefinition marked as a tenant
 * module. Enabled-state comes from the `TenantModule` CRD
 * (`core.cozystack.io/v1alpha1`), which is the canonical registry of modules
 * actually installed into a tenant. Matching uses the
 * `apps.cozystack.io/application.kind` label on the TenantModule so we don't
 * rely on name conventions.
 */
export function ModulesPage() {
  const { data: defs, isLoading: defsLoading } = useApplicationDefinitions()
  const { tenantNamespace, selectedTenant } = useTenantContext()

  const { data: tmList, isLoading: tmLoading } = useK8sList<K8sResource>(
    { ...TENANT_MODULES_REF, namespace: tenantNamespace ?? undefined },
    { enabled: !!tenantNamespace },
  )

  const modules = (defs?.items ?? [])
    .filter(isTenantModule)
    .sort((a, b) =>
      (a.spec?.application.kind ?? "").localeCompare(
        b.spec?.application.kind ?? "",
      ),
    )

  const isLoading = defsLoading || tmLoading

  const tmByKind = new Map<string, K8sResource>()
  for (const tm of tmList?.items ?? []) {
    const kind = tm.metadata.labels?.[APP_KIND_LABEL]
    if (kind) tmByKind.set(kind, tm)
  }

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 p-6 text-sm text-slate-500">
        <Spinner /> Loading modules…
      </div>
    )
  }

  return (
    <div className="p-6">
      <div className="mb-5">
        <h1 className="text-xl font-semibold text-slate-900">Modules</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Tenant-scoped add-ons, driven by the <code className="text-slate-700">TenantModule</code>
          {" "}registry in the selected tenant's namespace.
        </p>
      </div>

      {modules.length === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">No modules.</p>
        </Section>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {modules.map((ad) => (
            <ModuleCard
              key={ad.metadata.name}
              ad={ad}
              installed={tmByKind.get(ad.spec?.application.kind ?? "")}
              tenantName={selectedTenant ?? undefined}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function ModuleCard({
  ad,
  installed,
  tenantName,
}: {
  ad: ApplicationDefinition
  installed: K8sResource | undefined
  tenantName?: string
}) {
  const kind = ad.spec?.application.kind ?? ad.metadata.name
  const plural = ad.spec?.application.plural ?? ad.metadata.name
  const singletonName = installed?.metadata.name ?? kind.toLowerCase()
  const enabled = !!installed
  const canNavigate = enabled || !!tenantName
  const target = enabled
    ? `/console/${plural}/${singletonName}`
    : `/console/tenants/${tenantName}/edit`
  const icon = iconDataUrl(ad)

  return (
    <Link
      to={canNavigate ? target : "#"}
      aria-disabled={!canNavigate}
      className={`flex items-center gap-3 rounded-lg border border-slate-200 bg-white px-4 py-3 transition-shadow ${canNavigate ? "hover:shadow-sm" : "opacity-50 cursor-not-allowed pointer-events-none"}`}
    >
      <div className="size-10 shrink-0 overflow-hidden rounded-md bg-slate-100">
        {icon ? <img src={icon} alt="" className="h-full w-full" /> : null}
      </div>
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-semibold text-slate-900">
          {humanizeKind(kind)}
        </p>
        <p className="truncate text-xs text-slate-500">
          {ad.spec?.dashboard?.description ?? kind}
        </p>
      </div>
      <StatusBadge tone={enabled ? "ok" : "muted"}>
        {enabled ? <Check className="size-3" /> : <X className="size-3" />}
        {enabled ? "Enabled" : "Disabled"}
      </StatusBadge>
    </Link>
  )
}
