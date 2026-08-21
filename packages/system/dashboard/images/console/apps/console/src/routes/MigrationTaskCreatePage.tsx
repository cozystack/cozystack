import { useMemo, useRef, useState } from "react"
import { useNavigate } from "react-router"
import { Import, Save } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sCreate, useK8sList } from "@cozystack/k8s-client"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { useCRDSchema } from "../lib/use-crd-schema.ts"
import { SchemaForm, type SchemaFormHandle } from "../components/SchemaForm.tsx"
import {
  MIGRATION_GROUP,
  MIGRATION_VERSION,
  type MigrationResource,
  readyCondition,
} from "../lib/migration.ts"

export function MigrationTaskCreatePage() {
  const navigate = useNavigate()
  const { tenantNamespace } = useTenantContext()
  const [formData, setFormData] = useState<Record<string, unknown>>({})
  const [name, setName] = useState("")
  const schemaFormRef = useRef<SchemaFormHandle>(null)

  const { schema, isLoading: schemaLoading } = useCRDSchema("vmimporttasks.migration.cozystack.io")

  // sourceRef.name is a dropdown served by the vmimportsource option provider,
  // but the list is also read here to warn about a connection that is not ready
  // yet: starting an import against one only produces a task that sits Pending.
  const { data: sources } = useK8sList<MigrationResource>({
    apiGroup: MIGRATION_GROUP,
    apiVersion: MIGRATION_VERSION,
    plural: "vmimportsources",
    namespace: tenantNamespace ?? "",
  }, { enabled: !!tenantNamespace })

  const createMutation = useK8sCreate({
    apiGroup: MIGRATION_GROUP,
    apiVersion: MIGRATION_VERSION,
    plural: "vmimporttasks",
    namespace: tenantNamespace ?? "",
  })

  const selectedSource = (formData as { sourceRef?: { name?: string } }).sourceRef?.name
  const sourceWarning = useMemo(() => {
    const items = sources?.items ?? []
    if (items.length === 0) {
      return "No source connections exist yet. Add one before starting an import."
    }
    if (!selectedSource) return null
    const source = items.find((s) => s.metadata.name === selectedSource)
    if (!source) return null
    const ready = readyCondition(source)
    if (ready?.status === "True") return null
    return `Connection "${selectedSource}" is not ready${
      ready?.message ? `: ${ready.message}` : ""
    }. The import will wait until it is.`
  }, [sources, selectedSource])

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

    const spec = formData as { sourceRef?: { name?: string }; vms?: unknown[] }
    if (!spec.sourceRef?.name) {
      alert("A source connection is required")
      return
    }
    if (!spec.vms || spec.vms.length === 0) {
      alert("Add at least one machine to import")
      return
    }

    const resource = {
      apiVersion: `${MIGRATION_GROUP}/${MIGRATION_VERSION}`,
      kind: "VMImportTask",
      metadata: {
        name: name.trim(),
        namespace: tenantNamespace,
      },
      spec: formData,
    }

    try {
      await createMutation.mutateAsync(resource)
      navigate("/console/migration/vmimporttasks")
    } catch (err) {
      alert(`Failed to start the import: ${(err as Error).message}`)
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
        Failed to load the VMImportTask schema. Is the migration-controller package enabled?
      </div>
    )
  }

  return (
    <div className="p-6">
      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
          <Import className="size-6 text-slate-600" />
        </div>
        <div>
          <h1 className="text-lg font-semibold text-slate-900">New import</h1>
          <p className="text-xs text-slate-500">
            Name the machines to import. Each one becomes a virtual machine with its own
            disks, which stay after this import is deleted.
          </p>
        </div>
      </div>

      <Section>
        <div className="space-y-4 p-5">
          <div>
            <label htmlFor="task-name" className="mb-1 block text-sm font-medium text-slate-700">
              Import name <span className="text-red-500">*</span>
            </label>
            <input
              id="task-name"
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="import-web-tier"
              className="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400"
              required
            />
          </div>

          {sourceWarning && (
            <div className="rounded-lg border border-yellow-200 bg-yellow-50 px-3 py-2 text-xs text-yellow-800">
              {sourceWarning}
            </div>
          )}

          <div className="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-xs text-slate-600">
            Each machine is identified by its managed-object reference from vSphere
            (<code className="font-mono">vm-1234</code>) — visible in the vSphere client
            URL, or from <code className="font-mono">govc ls -i</code>.
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
                <Spinner /> Starting...
              </>
            ) : (
              <>
                <Save className="size-3.5" /> Start import
              </>
            )}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => navigate("/console/migration/vmimporttasks")}
            disabled={createMutation.isPending}
          >
            Cancel
          </Button>
        </div>
      </Section>
    </div>
  )
}
