import { useMemo } from "react"
import {
  Link,
  Navigate,
  Route,
  Routes,
  useNavigate,
  useParams,
} from "react-router"
import { ChevronLeft, Edit, Trash2 } from "lucide-react"
import { Button, Spinner, StatusBadge } from "@cozystack/ui"
import {
  useK8sGet,
  useK8sDelete,
} from "@cozystack/k8s-client"
import {
  APPS_GROUP,
  APPS_VERSION,
  type ApplicationInstance,
} from "@cozystack/types"
import { useApplicationDefinitions, isTenantModule } from "../../lib/app-definitions.ts"
import { useTenantContext } from "../../lib/tenant-context.tsx"
import {
  appDisplayName,
  iconDataUrl,
} from "../../lib/app-definitions.ts"
import { readyCondition } from "../../lib/status.ts"
import { TabBar } from "./tabs.tsx"
import { OverviewTab } from "./OverviewTab.tsx"
import { WorkloadsTab } from "./WorkloadsTab.tsx"
import { ServicesTab } from "./ServicesTab.tsx"
import { IngressesTab } from "./IngressesTab.tsx"
import { SecretsTab } from "./SecretsTab.tsx"
import { EventsTab } from "./EventsTab.tsx"
import { VncTab } from "./VncTab.tsx"
import { VMPowerControls } from "./VMPowerControls.tsx"
import { useResourceBasePath } from "../../lib/portal.ts"
import { useResourcePresence } from "./use-resource-presence.ts"

export function ApplicationDetailPage() {
  const { plural, name } = useParams<{ plural: string; name: string }>()
  const { data: defs } = useApplicationDefinitions()
  const { tenantNamespace } = useTenantContext()
  const navigate = useNavigate()
  const basePath = useResourceBasePath()

  const ad = useMemo(
    () =>
      defs?.items.find((d) => d.spec?.application.plural === plural),
    [defs, plural],
  )

  const { data: instance, isLoading, error } = useK8sGet<ApplicationInstance>(
    {
      apiGroup: APPS_GROUP,
      apiVersion: APPS_VERSION,
      plural: plural ?? "",
      name: name ?? "",
      namespace: tenantNamespace ?? "",
    },
    {
      enabled: !!plural && !!name && !!tenantNamespace,
      refetchInterval: 5000, // Auto-refresh every 5 seconds
    },
  )

  const del = useK8sDelete({
    apiGroup: APPS_GROUP,
    apiVersion: APPS_VERSION,
    plural: plural ?? "",
    namespace: tenantNamespace ?? undefined,
  })

  const presence = useResourcePresence(ad, instance)

  if (!plural || !name) return <Navigate to="/console" replace />
  // Check the fetch error before the loading guard: on a failed GET, isLoading
  // is false and instance stays undefined, so a !instance-first guard would
  // spin forever and never reach this branch.
  if (error) {
    return (
      <div className="p-8 text-red-600">
        Application <code>{name}</code> not found.
      </div>
    )
  }
  if (isLoading || !instance || !ad) {
    return (
      <div className="flex items-center gap-2 p-8 text-slate-500">
        <Spinner /> Loading…
      </div>
    )
  }

  const handleDelete = async () => {
    if (!confirm(`Delete ${appDisplayName(ad)} "${name}"? This cannot be undone.`)) return
    try {
      await del.mutateAsync(name)
      navigate(`${basePath}/${plural}`)
    } catch (err) {
      alert((err as Error).message)
    }
  }

  const ready = readyCondition(instance)
  const icon = iconDataUrl(ad)
  const base = `${basePath}/${plural}/${name}`
  const kind = ad.spec?.application.kind

  // Module singletons are reached from the admin trees (Info from Tenants,
  // every other module from Modules), not from an instance list — send Back
  // where the user came from instead of the meaningless one-item list.
  const backTarget = isTenantModule(ad)
    ? basePath === "/admin"
      ? kind === "Info"
        ? "/admin/tenants"
        : "/admin/modules"
      : basePath
    : `${basePath}/${plural}`

  // Absolute URLs so NavLink always rewrites the whole "/<plural>/<name>/..."
  // suffix instead of appending to the current tab path.
  const tabs = [{ to: base, label: "Overview", end: true }]

  // Different tab sets for different resource types
  if (kind === "VMDisk") {
    // VMDisk: storage-only resource, no workloads/services/ingresses/secrets
    tabs.push({ to: `${base}/events`, label: "Events", end: false })
  } else if (kind === "VMInstance") {
    // VMInstance: VM-specific tabs (no ingresses/secrets)
    tabs.push(
      { to: `${base}/workloads`, label: "Workloads", end: false },
      { to: `${base}/services`, label: "Services", end: false },
      { to: `${base}/events`, label: "Events", end: false },
      { to: `${base}/vnc`, label: "VNC", end: false },
    )
  } else {
    // Other resources: offer only the tabs the instance has content for.
    // Presence is watch-driven, so a tab appears as soon as the chart creates
    // its first matching resource (e.g. while an app is still provisioning).
    if (presence.workloads) {
      tabs.push({ to: `${base}/workloads`, label: "Workloads", end: false })
    }
    if (presence.services) {
      tabs.push({ to: `${base}/services`, label: "Services", end: false })
    }
    if (presence.ingresses) {
      tabs.push({ to: `${base}/ingresses`, label: "Ingresses", end: false })
    }
    tabs.push(
      { to: `${base}/secrets`, label: "Secrets", end: false },
      { to: `${base}/events`, label: "Events", end: false },
    )
  }

  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-slate-200 bg-white px-6 pt-4">
        <button
          type="button"
          onClick={() => navigate(backTarget)}
          className="mb-2 flex items-center gap-1 text-xs text-slate-500 hover:text-slate-900"
        >
          <ChevronLeft className="size-3.5" /> Back
        </button>
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="size-11 shrink-0 overflow-hidden rounded-md bg-slate-100">
              {icon ? <img src={icon} alt="" className="h-full w-full" /> : null}
            </div>
            <div>
              <p className="text-xs uppercase tracking-wider text-slate-500">
                {appDisplayName(ad)}
              </p>
              <h1 className="text-lg font-semibold text-slate-900">{name}</h1>
            </div>
            {instance?.metadata.deletionTimestamp ? (
              <StatusBadge tone="muted">Terminating</StatusBadge>
            ) : ready ? (
              <StatusBadge tone={ready.status === "True" ? "ok" : "warn"}>
                {ready.status === "True" ? "Ready" : (ready.reason ?? "NotReady")}
              </StatusBadge>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            {kind === "VMInstance" && (
              <VMPowerControls ad={ad} instance={instance} />
            )}
            <Link to={`${base}/edit`}>
              <Button variant="outline" size="sm">
                <Edit className="size-3.5" /> Edit
              </Button>
            </Link>
            <Button
              variant="destructive"
              size="sm"
              onClick={handleDelete}
              disabled={del.isPending}
            >
              <Trash2 className="size-3.5" /> Delete
            </Button>
          </div>
        </div>
        <div className="mt-3 -mb-px">
          <TabBar tabs={tabs} />
        </div>
      </div>

      <div className="flex-1 overflow-auto">
        <Routes>
          <Route index element={<OverviewTab ad={ad} instance={instance} />} />
          <Route
            path="workloads"
            element={<WorkloadsTab ad={ad} instance={instance} />}
          />
          <Route
            path="services"
            element={<ServicesTab ad={ad} instance={instance} />}
          />
          <Route
            path="ingresses"
            element={<IngressesTab ad={ad} instance={instance} />}
          />
          <Route
            path="secrets"
            element={<SecretsTab ad={ad} instance={instance} />}
          />
          <Route
            path="events"
            element={<EventsTab ad={ad} instance={instance} />}
          />
          <Route path="vnc" element={<VncTab ad={ad} instance={instance} />} />
        </Routes>
      </div>
    </div>
  )
}
