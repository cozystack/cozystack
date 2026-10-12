import { useRef, useState } from "react"
import { useNavigate } from "react-router"
import { Plug, Save } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sCreate } from "@cozystack/k8s-client"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { useCRDSchema } from "../lib/use-crd-schema.ts"
import { SchemaForm, type SchemaFormHandle } from "../components/SchemaForm.tsx"
import { MIGRATION_GROUP, MIGRATION_VERSION } from "../lib/migration.ts"

export function MigrationSourceCreatePage() {
  const navigate = useNavigate()
  const { tenantNamespace } = useTenantContext()
  const [formData, setFormData] = useState<Record<string, unknown>>({})
  const [name, setName] = useState("")
  const schemaFormRef = useRef<SchemaFormHandle>(null)

  const { schema, isLoading: schemaLoading } = useCRDSchema("vmimportsources.forklift.cozystack.io")

  const createMutation = useK8sCreate({
    apiGroup: MIGRATION_GROUP,
    apiVersion: MIGRATION_VERSION,
    plural: "vmimportsources",
    namespace: tenantNamespace ?? "",
  })

  const handleSubmit = async () => {
    if (!tenantNamespace) {
      alert("Tenant namespace is not available. Please refresh.")
      return
    }
    if (!name.trim()) {
      alert("Name is required")
      return
    }
    if (schemaFormRef.current && !schemaFormRef.current.validate()) return

    const resource = {
      apiVersion: `${MIGRATION_GROUP}/${MIGRATION_VERSION}`,
      kind: "VMImportSource",
      metadata: {
        name: name.trim(),
        namespace: tenantNamespace,
      },
      spec: formData,
    }

    try {
      await createMutation.mutateAsync(resource)
      navigate("/console/migration/vmimportsources")
    } catch (err) {
      alert(`Failed to create connection: ${(err as Error).message}`)
    }
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
        Failed to load the VMImportSource schema. Is the migration-controller package enabled?
      </div>
    )
  }

  return (
    <div className="p-6">
      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
          <Plug className="size-6 text-slate-600" />
        </div>
        <div>
          <h1 className="text-lg font-semibold text-slate-900">Add source connection</h1>
          <p className="text-xs text-slate-500">
            Register a vCenter to import machines from. The connection is tested before
            anything else happens.
          </p>
        </div>
      </div>

      <Section>
        <div className="space-y-4 p-5">
          <div>
            <label htmlFor="source-name" className="mb-1 block text-sm font-medium text-slate-700">
              Connection name <span className="text-red-500">*</span>
            </label>
            <input
              id="source-name"
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="vcenter-prod"
              className="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400"
              required
            />
          </div>

          <div>
            <SchemaForm
              ref={schemaFormRef}
              openAPISchema={schema}
              formData={formData}
              onChange={(data) => setFormData((data ?? {}) as Record<string, unknown>)}
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
            onClick={() => navigate("/console/migration/vmimportsources")}
            disabled={createMutation.isPending}
          >
            Cancel
          </Button>
        </div>
      </Section>
    </div>
  )
}
