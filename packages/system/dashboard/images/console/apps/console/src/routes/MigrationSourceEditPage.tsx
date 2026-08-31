import { useEffect, useRef, useState } from "react"
import { useNavigate, useParams } from "react-router"
import { Plug, Save } from "lucide-react"
import { Button, Section, Spinner } from "@cozystack/ui"
import { useK8sGet, useK8sUpdate } from "@cozystack/k8s-client"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { useCRDSchema } from "../lib/use-crd-schema.ts"
import { prepareUpdateSpec } from "../lib/prepare-update.ts"
import { SchemaForm, type SchemaFormHandle } from "../components/SchemaForm.tsx"
import {
  MIGRATION_GROUP,
  MIGRATION_VERSION,
  type MigrationResource,
} from "../lib/migration.ts"

/**
 * Editing a connection, which import tasks deliberately do not get.
 *
 * A source is long-lived: passwords rotate, a CA expires, and an ESXi host
 * moves to an address the cluster can reach. Without this page the only way to
 * change any of that is kubectl, which is not an answer for the audience this
 * API is for. A task is the opposite — a one-shot operation whose every spec
 * field is immutable on admission, so an edit form for one would offer changes
 * the API server rejects.
 */
export function MigrationSourceEditPage() {
  const { name } = useParams<{ name: string }>()
  const navigate = useNavigate()
  const { tenantNamespace } = useTenantContext()
  const [formData, setFormData] = useState<Record<string, unknown>>({})
  const initializedRef = useRef(false)
  // The spec as it was when the form initialised, so the immutable-field
  // overlay writes back what the user actually saw rather than whatever a
  // refetch produced in the meantime.
  const initialSpecRef = useRef<unknown>(null)
  const schemaFormRef = useRef<SchemaFormHandle>(null)

  const { schema, isLoading: schemaLoading } = useCRDSchema("vmimportsources.forklift.cozystack.io")

  const { data: resource, isLoading: resourceLoading, error } = useK8sGet<MigrationResource>(
    {
      apiGroup: MIGRATION_GROUP,
      apiVersion: MIGRATION_VERSION,
      plural: "vmimportsources",
      name: name ?? "",
      namespace: tenantNamespace ?? "",
    },
    { enabled: !!name && !!tenantNamespace },
  )

  const updateMutation = useK8sUpdate({
    apiGroup: MIGRATION_GROUP,
    apiVersion: MIGRATION_VERSION,
    plural: "vmimportsources",
    namespace: tenantNamespace ?? "",
  })

  // Initialised once: a refetch mid-edit must not discard what is being typed.
  useEffect(() => {
    if (resource?.spec && !initializedRef.current) {
      initializedRef.current = true
      setFormData(resource.spec)
      initialSpecRef.current = resource.spec
    }
  }, [resource])

  const handleSubmit = async () => {
    if (!resource || !schema) return

    // The save button sits outside RJSF and bypasses its validation, so ask
    // for it explicitly; an invalid spec renders inline and aborts the write.
    if (schemaFormRef.current && !schemaFormRef.current.validate()) return

    const updated = {
      ...resource,
      spec: prepareUpdateSpec(formData, initialSpecRef.current, schema),
    }

    try {
      await updateMutation.mutateAsync(updated)
      navigate("/console/migration/vmimportsources")
    } catch (err) {
      alert(`Failed to update connection: ${(err as Error).message}`)
    }
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
        Failed to load connection: {(error as Error).message}
      </div>
    )
  }

  if (!resource) {
    return <div className="p-8 text-red-600">Connection not found.</div>
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
          <Plug className="size-6 text-slate-600" />
        </div>
        <div>
          <h1 className="text-lg font-semibold text-slate-900">Edit connection</h1>
          <p className="text-xs text-slate-500">{name}</p>
        </div>
      </div>

      <Section>
        <div className="space-y-4 p-5">
          <p className="text-xs text-slate-500">
            Changing the credentials re-tests the connection. Imports already
            completed are not affected; an import still running keeps the
            credentials it started with.
          </p>
          <SchemaForm
            ref={schemaFormRef}
            openAPISchema={schema}
            formData={formData}
            onChange={(data) => setFormData((data ?? {}) as Record<string, unknown>)}
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
            onClick={() => navigate("/console/migration/vmimportsources")}
            disabled={updateMutation.isPending}
          >
            Cancel
          </Button>
        </div>
      </Section>
    </div>
  )
}
