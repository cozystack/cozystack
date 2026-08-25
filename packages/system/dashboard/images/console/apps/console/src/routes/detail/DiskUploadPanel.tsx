import { useState } from "react"
import { Check, Copy, RefreshCw } from "lucide-react"
import { Button, Section, StatusBadge } from "@cozystack/ui"
import { useK8sGet, useSelfSubjectAccessReview } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { releasePrefix } from "../../lib/app-definitions.ts"
import {
  isUploadSource,
  uploadState,
  usableProxyURL,
  virtctlUploadCommand,
  type CDIConfig,
  type DataVolume,
  type UploadStage,
} from "../../lib/vm-disk-upload.ts"

const CDI_GROUP = "cdi.kubevirt.io"
const CDI_VERSION = "v1beta1"
const UPLOAD_GROUP = "upload.cdi.kubevirt.io"

const STAGE_LABEL: Record<UploadStage, string> = {
  preparing: "Preparing upload target",
  "awaiting-upload": "Waiting for an image",
  succeeded: "Image uploaded",
  failed: "Upload failed",
  unknown: "Unknown",
}

const STAGE_TONE = {
  preparing: "info",
  "awaiting-upload": "warn",
  succeeded: "ok",
  failed: "error",
  unknown: "muted",
} as const

function CopyableCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false)
  // navigator.clipboard is absent on insecure origins; the <pre> stays selectable.
  const canCopy = !!navigator.clipboard
  const copy = async () => {
    if (!canCopy) return
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      setCopied(false)
    }
  }
  return (
    <div className="flex items-start gap-2">
      <pre className="flex-1 overflow-x-auto rounded-md bg-slate-900 px-3 py-2 text-xs leading-relaxed text-slate-100">
        {command}
      </pre>
      <Button
        variant="outline"
        size="sm"
        onClick={copy}
        disabled={!canCopy}
        aria-label="Copy command"
      >
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      </Button>
    </div>
  )
}

/**
 * Upload status for a VMDisk created with `source: upload`.
 *
 * Deliberately does not upload the file itself: the CDI upload proxy only ever
 * reads its token from the Authorization header, the Kubernetes API server
 * strips that header when proxying through services/proxy, and Cozystack
 * publishes cdi-uploadproxy with TLS passthrough so its internal self-signed
 * certificate reaches the browser directly. Neither route is usable from a
 * page, so the panel reports state and hands over the command that works.
 */
export function DiskUploadPanel({
  ad,
  instance,
}: {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}) {
  const ns = instance.metadata.namespace ?? ""
  const dvName = `${releasePrefix(ad)}${instance.metadata.name}`

  // Tenants may GET this DataVolume by name (the vm-disk chart grants exactly
  // that, via resourceNames) but may not list or watch datavolumes, so this is
  // a one-shot read with an explicit refresh rather than a watched list.
  const dvQuery = useK8sGet<DataVolume>(
    {
      apiGroup: CDI_GROUP,
      apiVersion: CDI_VERSION,
      plural: "datavolumes",
      name: dvName,
      namespace: ns,
    },
    {
      enabled: !!ns && !!instance.metadata.name,
      retry: false,
      // CDI walks the disk to UploadReady on its own and this panel never
      // unmounts, so without a poll the user sits on "preparing" until they
      // happen to press Refresh. Matches the detail page's instance poll.
      // Gated on the source too: the panel renders nothing for the other
      // vm-disk sources, and polling for a hidden panel is pure load.
      refetchInterval: (query) => {
        const dv = query.state.data
        return isUploadSource(dv) && uploadState(dv).stage === "preparing"
          ? 5000
          : false
      },
    },
  )

  // The proxy URL lives on the cluster-scoped CDIConfig singleton, and whether
  // a tenant may read it is up to what upstream CDI binds — this repo grants no
  // role of its own for it. So a failed read means "unknown", never "the
  // cluster published none". The field is empty anyway unless the kubevirt-cdi
  // chart was given uploadProxyURL.
  const cdiConfig = useK8sGet<CDIConfig>(
    {
      apiGroup: CDI_GROUP,
      apiVersion: CDI_VERSION,
      plural: "cdiconfigs",
      name: "config",
    },
    { retry: false },
  )

  const canUpload = useSelfSubjectAccessReview({
    resourceAttributes: {
      namespace: ns,
      group: UPLOAD_GROUP,
      resource: "uploadtokenrequests",
      verb: "create",
    },
  })

  const dv = dvQuery.data
  if (!isUploadSource(dv)) return null

  const state = uploadState(dv)
  const proxyURL = usableProxyURL(cdiConfig.data?.status?.uploadProxyURL)
  const command = virtctlUploadCommand({
    name: dvName,
    namespace: ns,
    uploadProxyURL: proxyURL,
  })
  // Only at awaiting-upload: `failed` covers PVC-bind failures where no upload
  // server was ever created, and CDI does not recreate one for a DataVolume
  // that already failed — the disk has to be recreated instead.
  const showCommand = state.stage === "awaiting-upload"

  return (
    <div className="px-6 pt-6">
      <Section
        title="Image upload"
        description="This disk was created from an uploaded image."
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              void dvQuery.refetch()
              void cdiConfig.refetch()
            }}
            disabled={dvQuery.isFetching || cdiConfig.isFetching}
          >
            <RefreshCw className="size-3.5" /> Refresh
          </Button>
        }
      >
        <div className="flex items-center gap-2">
          <StatusBadge tone={STAGE_TONE[state.stage]}>
            {STAGE_LABEL[state.stage]}
          </StatusBadge>
          {state.phase && (
            <span className="font-mono text-xs text-slate-500">{state.phase}</span>
          )}
          {state.progress && state.progress !== "N/A" && (
            <span className="tabular-nums text-xs text-slate-500">{state.progress}</span>
          )}
        </div>

        {state.stage === "preparing" && (
          <p className="mt-3 text-sm text-slate-600">
            CDI is provisioning the upload target. The disk accepts an image once it
            reaches <span className="font-mono text-xs">UploadReady</span>.
          </p>
        )}

        {state.stage === "succeeded" && (
          <p className="mt-3 text-sm text-slate-600">
            The image has been written to this disk. Re-uploading requires recreating
            the disk.
          </p>
        )}

        {state.stage === "failed" && (
          <p className="mt-3 text-sm text-slate-600">
            CDI does not retry a failed upload target. Recreate the disk to try
            again.
          </p>
        )}

        {state.message && (
          <p className="mt-3 text-sm text-red-700">{state.message}</p>
        )}

        {showCommand && (
          <div className="mt-4 space-y-2">
            <p className="text-sm text-slate-600">
              Uploading runs from your machine — the browser cannot reach the CDI
              upload proxy. Point <span className="font-mono text-xs">--image-path</span>{" "}
              at your local image and run:
            </p>
            <CopyableCommand command={command} />
            {!proxyURL && cdiConfig.isSuccess && (
              <p className="text-xs text-slate-500">
                This cluster publishes no upload proxy URL, so the address above is a
                placeholder. Ask your platform administrator for the{" "}
                <span className="font-mono">cdi-uploadproxy</span> hostname.
              </p>
            )}
            {/* An errored CDIConfig read is not an answer either way. */}
            {!proxyURL && cdiConfig.isError && (
              <p className="text-xs text-slate-500">
                The upload proxy URL could not be read from{" "}
                <span className="font-mono">CDIConfig</span>, so the address above is
                a placeholder. Refresh, or ask your platform administrator for the{" "}
                <span className="font-mono">cdi-uploadproxy</span> hostname.
              </p>
            )}
            {/* An errored SSAR surfaces as allowed=false, which is not a denial. */}
            {!canUpload.isLoading && !canUpload.error && !canUpload.allowed && (
              <p className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800">
                Your account cannot create{" "}
                <span className="font-mono">uploadtokenrequests</span> in this
                namespace, so this command will fail with Forbidden. Tenant roles do
                not yet carry the CDI upload permissions (cozystack/cozystack#3759) —
                a platform administrator has to run the upload for you.
              </p>
            )}
          </div>
        )}
      </Section>
    </div>
  )
}
