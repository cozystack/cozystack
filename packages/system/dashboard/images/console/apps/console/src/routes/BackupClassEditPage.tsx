import { useEffect, useRef, useState } from "react"
import { Link, useNavigate, useParams } from "react-router"
import { ArrowLeft, Archive, Save } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sGet, useK8sUpdate, type K8sResource } from "@cozystack/k8s-client"
import { useCRDSchema } from "../lib/use-crd-schema.ts"
import { prepareUpdateSpec } from "../lib/prepare-update.ts"
import { SchemaForm } from "../components/SchemaForm.tsx"

// BackupClass is cluster-scoped — editable by cluster admins, not tenant
// users. The API server's RBAC is the real gate; a tenant-scoped user's PUT
// here simply 403s and surfaces in the error alert.
export function BackupClassEditPage() {
  const { name } = useParams<{ name: string }>()
  const navigate = useNavigate()
  const [formData, setFormData] = useState<unknown>({})
  const initializedRef = useRef(false)
  // Snapshot of spec at form-init time, used as the immutable-overlay source
  // in prepareUpdateSpec so refetches can't change what gets PUT.
  const initialSpecRef = useRef<unknown>(null)

  const { schema, isLoading: schemaLoading } = useCRDSchema(
    "backupclasses.backups.cozystack.io",
  )

  const {
    data: resource,
    isLoading: resourceLoading,
    error,
  } = useK8sGet<K8sResource>(
    {
      apiGroup: "backups.cozystack.io",
      apiVersion: "v1alpha1",
      plural: "backupclasses",
      name: name ?? "",
    },
    { enabled: !!name },
  )

  const updateMutation = useK8sUpdate<K8sResource>({
    apiGroup: "backups.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "backupclasses",
  })

  // Initialise form data from the resource once, so a refetch mid-edit
  // doesn't clobber in-progress changes.
  useEffect(() => {
    if (resource?.spec && !initializedRef.current) {
      initializedRef.current = true
      setFormData(resource.spec)
      initialSpecRef.current = resource.spec
    }
  }, [resource])

  const detailPath = `/admin/backups/backupclasses/${name}`

  const handleSubmit = async () => {
    if (!resource || !schema) return
    const updated: K8sResource = {
      ...resource,
      spec: prepareUpdateSpec(formData, initialSpecRef.current, schema),
    }
    try {
      await updateMutation.mutateAsync(updated)
      navigate(detailPath)
    } catch (err) {
      alert(`Failed to update Backup Class: ${(err as Error).message}`)
    }
  }

  if (schemaLoading || resourceLoading) {
    return (
      <div className="flex items-center gap-2 p-8 text-slate-500">
        <Spinner /> Loading…
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-8 text-red-600">
        Failed to load Backup Class: {(error as Error).message}
      </div>
    )
  }

  if (!resource) {
    return <div className="p-8 text-red-600">Backup Class not found.</div>
  }

  if (!schema) {
    return (
      <div className="p-8 text-red-600">
        Failed to load schema. Please refresh the page.
      </div>
    )
  }

  return (
    <div className="p-6">
      <Link
        to={detailPath}
        className="mb-4 inline-flex items-center gap-1 text-xs text-slate-500 hover:text-slate-700"
      >
        <ArrowLeft className="size-3.5" /> {name}
      </Link>

      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
          <Archive className="size-6 text-slate-600" />
        </div>
        <div>
          <h1 className="text-lg font-semibold text-slate-900">Edit Backup Class</h1>
          <p className="text-xs text-slate-500">{name}</p>
        </div>
      </div>

      <Section>
        <div className="space-y-4 p-5">
          <SchemaForm
            openAPISchema={schema}
            formData={formData}
            onChange={setFormData}
            immutableMode="enforce"
          >
            <div className="hidden" />
          </SchemaForm>
        </div>

        <div className="flex items-center gap-2 border-t border-slate-200 px-5 py-3">
          <Button
            type="button"
            variant="primary"
            size="sm"
            onClick={handleSubmit}
            disabled={updateMutation.isPending}
          >
            {updateMutation.isPending ? (
              <>
                <Spinner /> Saving…
              </>
            ) : (
              <>
                <Save className="size-3.5" /> Save
              </>
            )}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => navigate(detailPath)}
            disabled={updateMutation.isPending}
          >
            Cancel
          </Button>
        </div>
      </Section>
    </div>
  )
}
