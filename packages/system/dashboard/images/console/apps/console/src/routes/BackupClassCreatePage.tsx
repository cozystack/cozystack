import { useState } from "react"
import { Link, useNavigate } from "react-router"
import { ArrowLeft, Archive, Save } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sCreate, type K8sResource } from "@cozystack/k8s-client"
import { useCRDSchema } from "../lib/use-crd-schema.ts"
import { SchemaForm } from "../components/SchemaForm.tsx"

// BackupClass is cluster-scoped; the guard wrapping this route already
// enforces that only an SSAR-allowed admin reaches it.
export function BackupClassCreatePage() {
  const navigate = useNavigate()
  const [formData, setFormData] = useState<unknown>({})
  const [name, setName] = useState("")

  const { schema, isLoading: schemaLoading } = useCRDSchema(
    "backupclasses.backups.cozystack.io",
  )

  const createMutation = useK8sCreate<K8sResource>({
    apiGroup: "backups.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "backupclasses",
  })

  const listPath = "/admin/backups/backupclasses"

  const handleSubmit = async () => {
    if (!name.trim()) {
      alert("Name is required")
      return
    }
    const resource: K8sResource = {
      apiVersion: "backups.cozystack.io/v1alpha1",
      kind: "BackupClass",
      metadata: { name: name.trim() },
      spec: formData,
    }
    try {
      await createMutation.mutateAsync(resource)
      navigate(`${listPath}/${encodeURIComponent(name.trim())}`)
    } catch (err) {
      alert(`Failed to create Backup Class: ${(err as Error).message}`)
    }
  }

  if (schemaLoading) {
    return (
      <div className="flex items-center gap-2 p-8 text-slate-500">
        <Spinner /> Loading schema…
      </div>
    )
  }

  if (!schema) {
    return (
      <div className="p-8 text-red-600">
        Failed to load Backup Class schema. Please refresh the page.
      </div>
    )
  }

  return (
    <div className="p-6">
      <Link
        to={listPath}
        className="mb-4 inline-flex items-center gap-1 text-xs text-slate-500 hover:text-slate-700"
      >
        <ArrowLeft className="size-3.5" /> Backups
      </Link>

      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
          <Archive className="size-6 text-slate-600" />
        </div>
        <div>
          <h1 className="text-lg font-semibold text-slate-900">Create Backup Class</h1>
          <p className="text-xs text-slate-500">
            Define a backup strategy for one or more application kinds
          </p>
        </div>
      </div>

      <Section>
        <div className="space-y-4 p-5">
          <div>
            <label className="mb-1 block text-sm font-medium text-slate-700">
              Name <span className="text-red-500">*</span>
            </label>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-backup-class"
              className="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400"
              required
            />
          </div>

          <div>
            <SchemaForm
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
                <Spinner /> Creating…
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
            onClick={() => navigate(listPath)}
            disabled={createMutation.isPending}
          >
            Cancel
          </Button>
        </div>
      </Section>
    </div>
  )
}
