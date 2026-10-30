import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react"
import { useNavigate, useParams, useSearchParams } from "react-router"
import { ChevronLeft, FileCode, FormInput } from "lucide-react"
import yaml from "js-yaml"
import { Button, Spinner, cn } from "@cozystack/ui"
import { useK8sClient, useK8sCreate, useK8sUpdate, K8sApiError } from "@cozystack/k8s-client"
import { APPS_GROUP, APPS_VERSION } from "@cozystack/types"
import {
  useApplicationDefinition,
  appDisplayName,
  iconDataUrl,
} from "../lib/app-definitions.ts"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { useResourceBasePath } from "../lib/portal.ts"
import { composeResource } from "../lib/app-resource.ts"
import { prepareUpdateSpec } from "../lib/prepare-update.ts"
import { isObject } from "../lib/server-form.ts"
import { SchemaForm, type SchemaFormHandle } from "../components/SchemaForm.tsx"

// Lazy-load the YAML editor so monaco and its workers are code-split into their
// own chunk, fetched only when the YAML view is opened.
const YamlEditor = lazy(() =>
  import("../components/YamlEditor.tsx").then((m) => ({ default: m.YamlEditor })),
)

type Mode = "form" | "yaml"

type Preview = { status: "loading" } | { status: "ready" } | { status: "failed"; error: string }

// The preview only has to pass the API's name checks, so it uses a short plain
// name unless the user came in with one.
const PREVIEW_NAME = "preview"
// Cap for a preview that gets no answer at all, such as a hanging admission
// webhook. An error response opens the form at once and does not wait for it.
const PREVIEW_TIMEOUT_MS = 8000

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
  const { tenantNamespace, isLoading: tenantLoading } = useTenantContext()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const basePath = useResourceBasePath()
  const client = useK8sClient()

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
  const isEditing = !!editMode
  const [preview, setPreview] = useState<Preview>({ status: "loading" })
  const [previewName, setPreviewName] = useState(searchParams.get("name") ?? "")
  const [previewSpec, setPreviewSpec] = useState<Record<string, unknown>>({})
  const [previewRun, setPreviewRun] = useState(0)
  const [loadedPlural, setLoadedPlural] = useState(plural)
  if (!isEditing && plural && loadedPlural !== plural) {
    setLoadedPlural(plural)
    setSpec({})
    setPreviewSpec({})
    setPreview({ status: "loading" })
    setMode("form")
    setYamlText("")
  }
  // Without a tenant the preview cannot start, so only a tenant list that is still
  // loading keeps the form waiting.
  const previewPending = !isEditing && preview.status === "loading" && (!!tenantNamespace || tenantLoading)

  const draft = useMemo(() => {
    try {
      let draftName = name
      let draftSpec = spec
      let document: Record<string, unknown> | null = null
      if (mode === "yaml") {
        const parsed = yaml.load(yamlText)
        if (!isObject(parsed)) throw new Error("Application YAML must be an object")
        document = parsed
        if (isObject(parsed.metadata) && typeof parsed.metadata.name === "string") draftName = parsed.metadata.name
        draftSpec = parsed.spec ?? {}
      }
      if (!isObject(draftSpec)) throw new Error("Application spec must be an object")
      const body = ad && tenantNamespace ? composeResource(ad, tenantNamespace, draftName, draftSpec) : null
      return { body, document, error: null }
    } catch (err) {
      return { body: null, document: null, error: (err as Error).message }
    }
  }, [ad, tenantNamespace, name, spec, mode, yamlText])
  // One dry-run create when the page opens: the server returns the spec with
  // installation and developer defaults, and the form starts from it. Edits do
  // not trigger more previews, the real create validates and applies defaults.
  useEffect(() => {
    if (isEditing || !ad || !tenantNamespace || !plural) return
    // Only a pending preview runs: a settled one must survive a tenant switch
    // without replacing what the user typed. Retry and a new kind set it pending.
    if (preview.status !== "loading") return
    let active = true
    let timer: ReturnType<typeof setTimeout> | undefined
    const body = composeResource(ad, tenantNamespace, previewName || PREVIEW_NAME, previewSpec)
    const timeout = new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error("the request timed out")), PREVIEW_TIMEOUT_MS)
    })
    Promise.race([client.dryRunCreate(APPS_GROUP, APPS_VERSION, plural, body, tenantNamespace), timeout])
      .then((result) => {
        const prepared = result.spec ?? {}
        if (!isObject(prepared)) throw new Error("the server returned a non-object application spec")
        if (!active) return
        setSpec(prepared)
        setPreview({ status: "ready" })
      })
      .catch((err: Error) => {
        if (!active) return
        setPreview({ status: "failed", error: err.message })
      })
      .finally(() => clearTimeout(timer))
    return () => {
      active = false
      clearTimeout(timer)
    }
  }, [client, ad, tenantNamespace, plural, isEditing, previewName, previewSpec, previewRun, preview.status])

  const retryPreview = () => {
    // The preview carries the current draft, so the answer completes it and keeps
    // what the user typed since the failure.
    const current = draft.body?.spec
    setPreviewSpec(isObject(current) ? current : {})
    setPreviewName(name)
    setPreview({ status: "loading" })
    setPreviewRun((run) => run + 1)
  }

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
      if (draft.error || !draft.body) throw new Error(draft.error ?? "Application is unavailable")
      setName(draft.body.metadata.name)
      setSpec(draft.body.spec ?? {})
      setYamlError(null)
      setMode("form")
    } catch (err) {
      setYamlError((err as Error).message)
    }
  }

  /** Build resource from whichever editor is currently active. */
  const snapshot = () => {
    if (draft.error || !draft.body) throw new Error(draft.error ?? "Application is unavailable")
    return { name: draft.body.metadata.name, spec: draft.body.spec ?? {} }
  }

  const submit = async () => {
    if (!ad || !tenantNamespace) return
    let snap: ReturnType<typeof snapshot>
    try { snap = snapshot() }
    catch (err) { setYamlError((err as Error).message); return }
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
      navigate(`${basePath}/${plural}/${snap.name}`)
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
          onClick={() => editMode ? navigate(`${basePath}/${plural}/${editMode.name}`) : navigate(-1)}
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
                disabled={previewPending}
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
                disabled={previewPending}
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
              onClick={() => editMode ? navigate(`${basePath}/${plural}/${editMode.name}`) : navigate(-1)}
              disabled={create.isPending || update.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={submit}
              disabled={create.isPending || update.isPending || !tenantNamespace || previewPending}
            >
              {(create.isPending || update.isPending) && <Spinner className="text-white" />}
              {editMode ? "Save" : "Deploy"}
            </Button>
          </div>
        </div>
      </div>

      {!isEditing && preview.status === "failed" && (
        <div role="alert" className="border-b border-amber-200 bg-amber-50 px-6 py-2 text-xs text-amber-800">
          Could not load the platform's initial settings ({preview.error}). The form starts empty, and the API still fills omitted fields when you deploy.
          {mode === "form" && <button type="button" onClick={retryPreview} className="ml-2 underline">Retry</button>}
        </div>
      )}

      {previewPending ? (
        <div role="status" className="flex items-center gap-2 p-8 text-slate-500">
          <Spinner /> Loading initial settings…
        </div>
      ) : mode === "form" ? (
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
                  defaulting="server"
                />
              </div>
            )}
          </div>
        </div>
      ) : (
        <div className="flex flex-1 flex-col bg-white">
          {(draft.error || yamlError) && (
            <div className="border-b border-red-200 bg-red-50 px-6 py-2 text-xs text-red-700">
              {draft.error ?? yamlError}
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
              <YamlEditor value={yamlText} onChange={(value) => { setYamlError(null); setYamlText(value) }} />
            </Suspense>
          </div>
        </div>
      )}
    </div>
  )
}
