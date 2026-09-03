import { useState, useMemo, useRef } from "react"
import { useNavigate } from "react-router"
import { Archive, Save } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sCreate, useK8sList } from "@cozystack/k8s-client"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { useApplicationDefinitions } from "../lib/app-definitions.ts"
import { useCRDSchema } from "../lib/use-crd-schema.ts"
import { SchemaForm, type SchemaFormHandle } from "../components/SchemaForm.tsx"
import { enrichSchemaWithEnums } from "../lib/backup-utils.ts"

export function BackupRestoreJobCreatePage() {
  const navigate = useNavigate()
  const { tenantNamespace } = useTenantContext()
  const { data: appDefs } = useApplicationDefinitions()
  const [formData, setFormData] = useState<any>({})
  const [name, setName] = useState("")
  const schemaFormRef = useRef<SchemaFormHandle>(null)

  // Get base schema from CRD
  const { schema: baseSchema, isLoading: schemaLoading } = useCRDSchema(
    "restorejobs.backups.cozystack.io"
  )

  // backupRef.name and targetApplicationRef.kind are dynamic dropdowns driven by
  // the CRD's x-cozystack-options keyword (backup / appkind sources), rendered
  // by DynamicOptionsWidget. Only targetApplicationRef.name stays on the
  // client-side enumMap below, because it depends on the chosen kind — a context
  // the Option contract cannot express.

  // Get instances for selected target kind.
  // Strict undefined check so an explicit empty string from the user means
  // "no group" — clearing the field opts out of the cozystack defaults.
  const selectedKind = formData?.targetApplicationRef?.kind
  const rawApiGroup = formData?.targetApplicationRef?.apiGroup
  const selectedApiGroup = rawApiGroup === undefined ? "apps.cozystack.io" : rawApiGroup
  const selectedAppDef = useMemo(
    () => appDefs?.items.find(d => d.spec?.application.kind === selectedKind),
    [appDefs, selectedKind]
  )

  const { data: instancesData } = useK8sList<any>({
    apiGroup: "apps.cozystack.io",
    apiVersion: "v1alpha1",
    plural: selectedAppDef?.spec?.application.plural ?? "",
    namespace: tenantNamespace ?? "",
  }, { enabled: !!selectedAppDef && !!tenantNamespace && selectedApiGroup === "apps.cozystack.io" })

  const createMutation = useK8sCreate({
    apiGroup: "backups.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "restorejobs",
    namespace: tenantNamespace ?? "",
  })

  const schema = useMemo(() => {
    if (!baseSchema) return null

    let base
    try {
      base = JSON.parse(baseSchema)
    } catch (e) {
      console.error("Failed to parse RestoreJob schema:", e)
      return null
    }
    const instances = instancesData?.items.map((inst: any) => inst.metadata.name) ?? []

    const enumMap: Record<string, string[]> = {}

    // targetApplicationRef.name depends on the selected kind, so it cannot be
    // served by the Option contract — keep it on the client-side enumMap. Only
    // fill it when the cozystack apiGroup matches (ApplicationDefinitions cover
    // apps.cozystack.io only — for any other apiGroup the name field has no enum
    // and stays free-text). The kind itself is always an appkind dropdown.
    if (selectedApiGroup === "apps.cozystack.io" && selectedKind && instances.length > 0) {
      enumMap["targetApplicationRef.name"] = instances
    }

    // Enrich schema with enum values
    const enriched = enrichSchemaWithEnums(base, [], enumMap)

    // Add default value for apiGroup
    if (enriched.properties?.targetApplicationRef?.properties?.apiGroup) {
      enriched.properties.targetApplicationRef.properties.apiGroup.default = "apps.cozystack.io"
    }

    // The CRD ships backupRef.name with `default: ""` (k8s LocalObjectReference
    // convention). Strip it so the DynamicOptionsWidget starts from a clean
    // empty value and the chosen value is the single source of truth, rather
    // than RJSF re-applying the empty default on re-render.
    if (enriched.properties?.backupRef?.properties?.name?.default !== undefined) {
      delete enriched.properties.backupRef.properties.name.default
    }

    // spec.options is a driver-specific blob — the CRD declares it as
    // `type: object` + `x-kubernetes-preserve-unknown-fields: true`, which
    // sanitizeSchema flattens to `additionalProperties: true`. RJSF then has
    // no widget for it. Rewrite to a typed map so AdditionalPropertiesField
    // auto-attaches and the user gets a key/value editor.
    if (enriched.properties?.options) {
      delete enriched.properties.options["x-kubernetes-preserve-unknown-fields"]
      enriched.properties.options.type = "object"
      enriched.properties.options.additionalProperties = { type: "string" }
      enriched.properties.options.properties = enriched.properties.options.properties ?? {}
    }

    return JSON.stringify(enriched)
  }, [baseSchema, instancesData, selectedKind, selectedApiGroup])

  const handleSubmit = async () => {
    if (!tenantNamespace) {
      alert("Tenant namespace is not available. Please refresh.")
      return
    }

    if (!name.trim()) {
      alert("Name is required")
      return
    }

    // Run RJSF validation before the page-level checks so schema-required
    // fields render inline errors instead of being masked by the alerts below.
    if (schemaFormRef.current && !schemaFormRef.current.validate()) return

    if (!formData.backupRef?.name) {
      alert("Backup reference is required")
      return
    }

    // targetApplicationRef is optional in the CRD — when omitted, the driver
    // restores into the same application referenced by the backup. Reject
    // partial input (kind without name or vice versa), but accept an empty ref.
    const target = formData.targetApplicationRef
    const hasTargetKind = !!target?.kind
    const hasTargetName = !!target?.name
    if (hasTargetKind !== hasTargetName) {
      alert("Target reference requires both Kind and Name, or leave both empty to restore into the source application")
      return
    }

    // Strip an empty targetApplicationRef so the API does not receive an empty
    // object that the API server would reject as malformed.
    const spec = { ...formData }
    if (!hasTargetKind && !hasTargetName) {
      delete spec.targetApplicationRef
    }

    const resource = {
      apiVersion: "backups.cozystack.io/v1alpha1",
      kind: "RestoreJob",
      metadata: {
        name: name.trim(),
        namespace: tenantNamespace ?? undefined,
      },
      spec,
    }

    try {
      await createMutation.mutateAsync(resource)
      navigate("/console/backups/restorejobs")
    } catch (err) {
      alert(`Failed to create RestoreJob: ${(err as Error).message}`)
    }
  }

  const handleCancel = () => {
    navigate("/console/backups/restorejobs")
  }

  if (schemaLoading) {
    return (
      <div className="flex items-center gap-2 p-8 text-slate-500">
        <Spinner /> Loading schema...
      </div>
    )
  }

  if (!schema) {
    return (
      <div className="p-8 text-red-600">
        Failed to load RestoreJob schema. Please refresh the page.
      </div>
    )
  }

  return (
    <div className="p-6">
      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
          <Archive className="size-6 text-slate-600" />
        </div>
        <div>
          <h1 className="text-lg font-semibold text-slate-900">Create Restore Job</h1>
          <p className="text-xs text-slate-500">
            Restore a backup to an application instance
          </p>
        </div>
      </div>

      <div>
        <Section>
          <div className="space-y-4 p-5">
            <div>
              <label className="block text-sm font-medium text-slate-700 mb-1">
                Restore Job Name <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="my-restore-job"
                className="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400"
                required
              />
            </div>

            <div>
              <SchemaForm
                ref={schemaFormRef}
                openAPISchema={schema}
                formData={formData}
                onChange={setFormData}
              >
                <div className="hidden" />
              </SchemaForm>
            </div>
          </div>

          <div className="flex items-center gap-2 border-t border-slate-200 px-5 py-3">
            <Button
              type="button"
              variant="primary"
              size="sm"
              onClick={handleSubmit}
              disabled={createMutation.isPending}
            >
              {createMutation.isPending ? (
                <>
                  <Spinner /> Creating...
                </>
              ) : (
                <>
                  <Save className="size-3.5" /> Create
                </>
              )}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={handleCancel}
              disabled={createMutation.isPending}
            >
              Cancel
            </Button>
          </div>
        </Section>
      </div>
    </div>
  )
}
