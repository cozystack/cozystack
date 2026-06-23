import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react"
import { useNavigate, useParams, useSearchParams } from "react-router"
import { ChevronLeft, FileCode, FormInput } from "lucide-react"
import yaml from "js-yaml"
import { Button, Spinner, cn } from "@cozystack/ui"
import { useK8sCreate, useK8sUpdate, K8sApiError } from "@cozystack/k8s-client"
import { APPS_GROUP, APPS_VERSION } from "@cozystack/types"
import {
  useApplicationDefinition,
  appDisplayName,
  iconDataUrl,
} from "../lib/app-definitions.ts"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { composeResource } from "../lib/app-resource.ts"
import { prepareUpdateSpec } from "../lib/prepare-update.ts"
import { SchemaForm, type SchemaFormHandle } from "../components/SchemaForm.tsx"

// Lazy-load the YAML editor so monaco and its workers are code-split into their
// own chunk, fetched only when the YAML view is opened.
const YamlEditor = lazy(() =>
  import("../components/YamlEditor.tsx").then((m) => ({ default: m.YamlEditor })),
)

type Mode = "form" | "yaml"

interface ApplicationOrderPageProps {
  /** When provided, the page pre-fills the form with this spec and PUTs on save. */
  editMode?: { name: string; initialSpec: unknown }
  /** Override the AD name that would otherwise come from the URL. */
  appNameOverride?: string
}

export function ApplicationOrderPage({
  editMode,
  appNameOverride,
}: ApplicationOrderPageProps) {
  const routeParams = useParams<{ appName: string }>()
  const appName = appNameOverride ?? routeParams.appName
  const { data: ad, isLoading, error } = useApplicationDefinition(appName)
  const { tenantNamespace } = useTenantContext()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()

  const [name, setName] = useState(editMode?.name ?? searchParams.get("name") ?? "")
  const [spec, setSpec] = useState<unknown>(editMode?.initialSpec ?? {})
  const [mode, setMode] = useState<Mode>("form")
  const [yamlText, setYamlText] = useState("")
  const [yamlError, setYamlError] = useState<string | null>(null)
  // Snapshot of the persisted spec captured the first time we see an
  // editMode prop. ApplicationEditRoute reconstructs `editMode` on every
  // React-Query refetch, so reading `editMode.initialSpec` at save time
  // would pick up whatever the cluster has right now, not what the user
  // saw when they opened the form. The ref locks the overlay source to
  // the value the user actually viewed.
  const initialSpecRef = useRef<unknown>(editMode?.initialSpec)
  const initialSpecCapturedRef = useRef(false)
  const schemaFormRef = useRef<SchemaFormHandle>(null)
  useEffect(() => {
    if (editMode && !initialSpecCapturedRef.current) {
      initialSpecCapturedRef.current = true
      initialSpecRef.current = editMode.initialSpec
    }
  }, [editMode])

  const plural = ad?.spec?.application.plural ?? ""

  const create = useK8sCreate({
    apiGroup: APPS_GROUP,
    apiVersion: APPS_VERSION,
    plural,
    namespace: tenantNamespace ?? undefined,
  })
  const update = useK8sUpdate({
    apiGroup: APPS_GROUP,
    apiVersion: APPS_VERSION,
    plural,
    namespace: tenantNamespace ?? undefined,
  })

  const resource = useMemo(() => {
    if (!ad || !tenantNamespace) return null
    return composeResource(ad, tenantNamespace, name || "<name>", spec)
  }, [ad, tenantNamespace, name, spec])

  /** Switch into YAML mode: serialize current state. */
  const enterYaml = () => {
    if (resource) setYamlText(yaml.dump(resource))
    setYamlError(null)
    setMode("yaml")
  }

  /** Switch into form mode: parse YAML back to name/spec. */
  const enterForm = () => {
    try {
      const parsed = yaml.load(yamlText) as {
        metadata?: { name?: string }
        spec?: unknown
      }
      if (parsed?.metadata?.name) setName(parsed.metadata.name)
      if (parsed?.spec !== undefined) setSpec(parsed.spec)
      setYamlError(null)
      setMode("form")
    } catch (err) {
      setYamlError((err as Error).message)
    }
  }

  /** Build resource from whichever editor is currently active. */
  const snapshot = () => {
    if (mode === "yaml") {
      const parsed = yaml.load(yamlText) as {
        metadata?: { name?: string }
        spec?: unknown
      }
      return {
        name: parsed?.metadata?.name ?? name,
        spec: parsed?.spec ?? spec,
      }
    }
    return { name, spec }
  }

  const submit = async () => {
    if (!ad || !tenantNamespace) return
    const snap = snapshot()
    if (!snap.name) {
      alert("Please set a resource name.")
      return
    }
    // The Deploy button lives outside RJSF and bypasses its submit, so trigger
    // validation explicitly in form mode. RJSF renders the errors inline; abort
    // so an invalid spec (e.g. a disk row left without a name) is never sent to
    // the API. YAML mode hand-authors the spec and is left to the API to reject.
    if (mode === "form" && schemaFormRef.current && !schemaFormRef.current.validate()) {
      return
    }
    const body = composeResource(ad, tenantNamespace, snap.name, snap.spec)
    try {
      if (editMode) {
        // initialSpecRef holds the value the user saw when the form
        // opened; if the resource is mutated externally between mount and
        // Save, the overlay reinstates the mount-time value. That's the
        // documented trade-off — re-mount to pick up fresh state.
        body.spec = prepareUpdateSpec<Record<string, unknown>>(
          body.spec ?? {},
          (initialSpecRef.current ?? {}) as Record<string, unknown>,
          ad.spec?.application.openAPISchema ?? "",
        )
        await update.mutateAsync(body)
      } else {
        await create.mutateAsync(body)
      }
      navigate(`/console/${plural}/${snap.name}`)
    } catch (err) {
      if (err instanceof K8sApiError) {
        alert(`Failed: ${err.message}`)
      } else {
        alert(`Failed: ${(err as Error).message}`)
      }
    }
  }

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 p-8 text-slate-500">
        <Spinner /> Loading application…
      </div>
    )
  }
  if (error || !ad) {
    return (
      <div className="p-8 text-red-600">
        Application <code>{appName}</code> not found.
      </div>
    )
  }

  const icon = iconDataUrl(ad)
  const displayName = appDisplayName(ad)
  const description = ad.spec?.dashboard?.description
  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-slate-200 bg-white px-6 pt-4 pb-3">
        <button
          type="button"
          onClick={() => editMode ? navigate(`/console/${plural}/${editMode.name}`) : navigate(-1)}
          className="mb-2 flex items-center gap-1 text-xs text-slate-500 hover:text-slate-900"
        >
          <ChevronLeft className="size-3.5" /> Back
        </button>
        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="size-11 shrink-0 overflow-hidden rounded-md bg-slate-100">
              {icon ? (
                <img src={icon} alt={displayName} className="h-full w-full" />
              ) : null}
            </div>
            <div>
              <p className="text-xs uppercase tracking-wider text-slate-500">
                {displayName}
              </p>
              <h1 className="text-lg font-semibold text-slate-900">
                {editMode ? `Edit ${editMode.name}` : `Deploy new ${ad.spec?.application.singular ?? "instance"}`}
              </h1>
              {description && (
                <p className="mt-0.5 text-xs text-slate-500">{description}</p>
              )}
            </div>
          </div>
          <div className="flex items-center gap-2">
            <div className="inline-flex rounded-lg border border-slate-200 bg-slate-50 p-0.5">
              <button
                type="button"
                aria-pressed={mode === "form"}
                onClick={() => (mode === "yaml" ? enterForm() : undefined)}
                className={cn(
                  "flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium transition-all",
                  mode === "form"
                    ? "bg-white text-blue-700 shadow-sm ring-1 ring-slate-200"
                    : "text-slate-500 hover:text-slate-700",
                )}
              >
                <FormInput className="size-3.5" /> Form
              </button>
              <button
                type="button"
                aria-pressed={mode === "yaml"}
                onClick={() => (mode === "form" ? enterYaml() : undefined)}
                className={cn(
                  "flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium transition-all",
                  mode === "yaml"
                    ? "bg-white text-blue-700 shadow-sm ring-1 ring-slate-200"
                    : "text-slate-500 hover:text-slate-700",
                )}
              >
                <FileCode className="size-3.5" /> YAML
              </button>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => editMode ? navigate(`/console/${plural}/${editMode.name}`) : navigate(-1)}
              disabled={create.isPending || update.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={submit}
              disabled={create.isPending || update.isPending || !tenantNamespace}
            >
              {(create.isPending || update.isPending) && <Spinner className="text-white" />}
              {editMode ? "Save" : "Deploy"}
            </Button>
          </div>
        </div>
      </div>

      {mode === "form" ? (
        <div className="flex-1 overflow-auto bg-slate-50 p-4">
          <div className="space-y-3">
            <div className="rounded-lg border border-slate-200 bg-white p-4">
              <label className="mb-1 block text-xs font-medium text-slate-600">
                Name <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={!!editMode}
                placeholder={ad.spec?.application.singular ?? "name"}
                className="w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition-shadow focus:border-blue-500 focus:ring-2 focus:ring-blue-500/20 disabled:bg-slate-50 disabled:text-slate-400"
              />
              {tenantNamespace && (
                <p className="mt-1.5 text-xs text-slate-500">
                  Namespace: <code className="text-slate-700">{tenantNamespace}</code>
                </p>
              )}
            </div>
            {ad.spec?.application.openAPISchema && (
              <div className="rounded-lg border border-slate-200 bg-white p-4">
                <SchemaForm
                  ref={schemaFormRef}
                  openAPISchema={ad.spec.application.openAPISchema}
                  keysOrder={ad.spec?.dashboard?.keysOrder}
                  formData={spec}
                  onChange={setSpec}
                  immutableMode={editMode ? "enforce" : "off"}
                />
              </div>
            )}
          </div>
        </div>
      ) : (
        <div className="flex flex-1 flex-col bg-white">
          {yamlError && (
            <div className="border-b border-red-200 bg-red-50 px-6 py-2 text-xs text-red-700">
              {yamlError}
            </div>
          )}
          <div className="flex-1">
            <Suspense
              fallback={
                <div className="flex h-full items-center justify-center">
                  <Spinner />
                </div>
              }
            >
              <YamlEditor value={yamlText} onChange={setYamlText} />
            </Suspense>
          </div>
        </div>
      )}
    </div>
  )
}
