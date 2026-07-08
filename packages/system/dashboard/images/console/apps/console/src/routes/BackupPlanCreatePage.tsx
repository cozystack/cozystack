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

export function BackupPlanCreatePage() {
  const navigate = useNavigate()
  const { tenantNamespace } = useTenantContext()
  const { data: appDefs } = useApplicationDefinitions()
  const [formData, setFormData] = useState<any>({})
  const [name, setName] = useState("")
  const schemaFormRef = useRef<SchemaFormHandle>(null)

  // Get base schema from CRD
  const { schema: baseSchema, isLoading: schemaLoading } = useCRDSchema(
    "plans.backups.cozystack.io"
  )

  // backupClassName and applicationRef.kind are dynamic dropdowns driven by the
  // CRD's x-cozystack-options keyword (backupclass / appkind sources), rendered
  // by DynamicOptionsWidget. Only applicationRef.name stays on the client-side
  // enumMap below, because it depends on the chosen kind — a context the Option
  // contract cannot express.

  // Get instances for selected kind.
  // Strict undefined check so an explicit empty string from the user means
  // "no group" — clearing the field opts out of the cozystack defaults.
  const selectedKind = formData?.applicationRef?.kind
  const rawApiGroup = formData?.applicationRef?.apiGroup
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
    plural: "plans",
    namespace: tenantNamespace ?? "",
  })

  const schema = useMemo(() => {
    if (!baseSchema) return null

    let base
    try {
      base = JSON.parse(baseSchema)
    } catch (e) {
      console.error("Failed to parse Plan schema:", e)
      return null
    }
    const instances = instancesData?.items.map((inst: any) => inst.metadata.name) ?? []

    const enumMap: Record<string, string[]> = {}

    // applicationRef.name depends on the selected kind, so it cannot be served
    // by the Option contract — keep it on the client-side enumMap. Only fill it
    // when the cozystack apiGroup matches (ApplicationDefinitions cover it).
    if (selectedApiGroup === "apps.cozystack.io" && selectedKind && instances.length > 0) {
      enumMap["applicationRef.name"] = instances
    }

    // Enrich schema with enum values
    const enriched = enrichSchemaWithEnums(base, [], enumMap)

    // Add default value for apiGroup
    if (enriched.properties?.applicationRef?.properties?.apiGroup) {
      enriched.properties.applicationRef.properties.apiGroup.default = "apps.cozystack.io"
    }

    // Pre-fill cron with a daily 02:00 default so the user starts with a
    // valid expression they can edit, rather than an empty required field.
    if (enriched.properties?.schedule?.properties?.cron) {
      enriched.properties.schedule.properties.cron.default = "0 2 * * *"
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

    if (!formData.applicationRef?.kind || !formData.applicationRef?.name) {
      alert("Application reference is required")
      return
    }

    if (!formData.backupClassName) {
      alert("Backup class name is required")
      return
    }

    // The submit button lives outside RJSF and bypasses its validation, so
    // trigger it explicitly; an invalid spec renders errors inline and aborts.
    if (schemaFormRef.current && !schemaFormRef.current.validate()) return

    const resource = {
      apiVersion: "backups.cozystack.io/v1alpha1",
      kind: "Plan",
      metadata: {
        name: name.trim(),
        namespace: tenantNamespace ?? undefined,
      },
      spec: formData,
    }

    try {
      await createMutation.mutateAsync(resource)
      navigate("/console/backups/plans")
    } catch (err) {
      alert(`Failed to create Plan: ${(err as Error).message}`)
    }
  }

  const handleCancel = () => {
    navigate("/console/backups/plans")
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
        Failed to load Plan schema. Please refresh the page.
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
          <h1 className="text-lg font-semibold text-slate-900">Create Plan</h1>
          <p className="text-xs text-slate-500">
            Configure a backup plan for your application
          </p>
        </div>
      </div>

      <div>
        <Section>
          <div className="space-y-4 p-5">
            <div>
              <label className="block text-sm font-medium text-slate-700 mb-1">
                Plan Name <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="my-backup-plan"
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
