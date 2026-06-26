import { useEffect, useRef, useState } from "react"
import { useNavigate, useParams } from "react-router"
import { Archive, Save } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sGet, useK8sUpdate } from "@cozystack/k8s-client"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { useCRDSchema } from "../lib/use-crd-schema.ts"
import { prepareUpdateSpec } from "../lib/prepare-update.ts"
import { SchemaForm, type SchemaFormHandle } from "../components/SchemaForm.tsx"

interface BackupResourceEditPageProps {
  resourceType: "plans" | "backupjobs" | "backups" | "restorejobs"
  title: string
  overrideSchema?: string
}

export function BackupResourceEditPage({
  resourceType,
  title,
  overrideSchema,
}: BackupResourceEditPageProps) {
  const { name } = useParams<{ name: string }>()
  const navigate = useNavigate()
  const { tenantNamespace } = useTenantContext()
  const [formData, setFormData] = useState<any>({})
  const initializedRef = useRef(false)
  // Snapshot of resource.spec at the moment the form initialised. Used as
  // the source for the immutable-field overlay so the value the user saw
  // in the form is the value that goes into the PUT, regardless of any
  // React-Query refetches in between.
  const initialSpecRef = useRef<unknown>(null)
  const schemaFormRef = useRef<SchemaFormHandle>(null)

  // Map resourceType to CRD name
  const crdNameMap = {
    plans: "plans.backups.cozystack.io",
    backupjobs: "backupjobs.backups.cozystack.io",
    backups: "backups.backups.cozystack.io",
    restorejobs: "restorejobs.backups.cozystack.io",
  }

  const { schema: crdSchema, isLoading: schemaLoading } = useCRDSchema(crdNameMap[resourceType])

  // Use override schema if provided, otherwise use CRD schema
  const schema = overrideSchema || crdSchema

  // Fetch existing resource
  const { data: resource, isLoading: resourceLoading, error } = useK8sGet<any>(
    {
      apiGroup: "backups.cozystack.io",
      apiVersion: "v1alpha1",
      plural: resourceType,
      name: name ?? "",
      namespace: tenantNamespace ?? "",
    },
    { enabled: !!name && !!tenantNamespace },
  )

  const updateMutation = useK8sUpdate({
    apiGroup: "backups.cozystack.io",
    apiVersion: "v1alpha1",
    plural: resourceType,
    namespace: tenantNamespace ?? "",
  })

  // Initialize form data from resource only once to avoid overwriting in-progress edits on refetch
  useEffect(() => {
    if (resource?.spec && !initializedRef.current) {
      initializedRef.current = true
      setFormData(resource.spec)
      initialSpecRef.current = resource.spec
    }
  }, [resource])

  const handleSubmit = async () => {
    if (!resource) return
    if (!schema) return

    // The submit button lives outside RJSF and bypasses its validation, so
    // trigger it explicitly; an invalid spec renders errors inline and aborts.
    if (schemaFormRef.current && !schemaFormRef.current.validate()) return

    const updated = {
      ...resource,
      spec: prepareUpdateSpec(formData, initialSpecRef.current, schema),
    }

    try {
      await updateMutation.mutateAsync(updated)
      navigate(`/console/backups/${resourceType}`)
    } catch (err) {
      alert(`Failed to update ${title.slice(0, -1)}: ${(err as Error).message}`)
    }
  }

  const handleCancel = () => {
    navigate(`/console/backups/${resourceType}`)
  }

  if (schemaLoading || resourceLoading) {
    return (
      <div className="flex items-center gap-2 p-8 text-slate-500">
        <Spinner /> Loading...
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-8 text-red-600">
        Failed to load {title.slice(0, -1)}: {(error as Error).message}
      </div>
    )
  }

  if (!resource) {
    return (
      <div className="p-8 text-red-600">
        {title.slice(0, -1)} not found.
      </div>
    )
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
      <div className="mb-5 flex items-center gap-3">
        <div className="flex size-11 shrink-0 items-center justify-center rounded-md bg-slate-100">
          <Archive className="size-6 text-slate-600" />
        </div>
        <div>
          <h1 className="text-lg font-semibold text-slate-900">
            Edit {title.slice(0, -1)}
          </h1>
          <p className="text-xs text-slate-500">
            {name}
          </p>
        </div>
      </div>

      <div>
        <Section>
          <div className="space-y-4 p-5">
            {schema && (
              <div>
                <SchemaForm
                  ref={schemaFormRef}
                  openAPISchema={schema}
                  formData={formData}
                  onChange={setFormData}
                  immutableMode="enforce"
                >
                  <div className="hidden" />
                </SchemaForm>
              </div>
            )}
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
                  <Spinner /> Saving...
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
              onClick={handleCancel}
              disabled={updateMutation.isPending}
            >
              Cancel
            </Button>
          </div>
        </Section>
      </div>
    </div>
  )
}
