import { useState } from "react"
import { Check, Copy, RefreshCw } from "lucide-react"
import { Button, Section, StatusBadge } from "@cozystack/ui"
import {
  useK8sGet,
  useK8sList,
  useSelfSubjectAccessReview,
} from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { releasePrefix } from "@/lib/app-definitions.ts"
import { readyCondition } from "@/lib/status.ts"
import {
  dataVolumeCapacity,
  isUploadSource,
  proxyURLFromCDIConfig,
  uploadState,
  virtctlUploadCommand,
} from "@/lib/vm-disk-upload.ts"
import type {
  CDIConfig,
  DataVolume,
  UploadStage,
  UploadState,
} from "@/lib/vm-disk-upload.ts"

const CDI_GROUP = "cdi.kubevirt.io"
const CDI_VERSION = "v1beta1"
const UPLOAD_GROUP = "upload.cdi.kubevirt.io"

const STAGE_LABEL: Record<UploadStage, string> = {
  preparing: "Preparing upload target",
  "awaiting-upload": "Upload target ready",
  paused: "Upload paused",
  succeeded: "Image uploaded",
  failed: "Upload failed",
  unknown: "Unknown",
}

const STAGE_TONE = {
  preparing: "info",
  "awaiting-upload": "warn",
  paused: "warn",
  succeeded: "ok",
  failed: "error",
  unknown: "muted",
} as const

function CopyableCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false)
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
        aria-label={copied ? "Command copied" : "Copy command"}
      >
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      </Button>
    </div>
  )
}

function requestRetry(failureCount: number, error: unknown): boolean {
  if (
    typeof error === "object" &&
    error !== null &&
    "status" in error &&
    (error as { status?: number }).status === 401
  ) {
    return false
  }
  return failureCount < 3
}

function UploadPrerequisites({
  name,
  namespace,
}: {
  name: string
  namespace: string
}) {
  const cdiConfig = useK8sGet<CDIConfig>(
    {
      apiGroup: CDI_GROUP,
      apiVersion: CDI_VERSION,
      plural: "cdiconfigs",
      name: "config",
    },
    { refetchOnMount: "always", retry: false },
  )
  const canCreateToken = useSelfSubjectAccessReview({
    resourceAttributes: {
      namespace,
      group: UPLOAD_GROUP,
      resource: "uploadtokenrequests",
      verb: "create",
    },
  })
  // Populator uploads target a dynamically named PVC Prime, so a named-PVC
  // grant is insufficient even though the first virtctl read uses `name`.
  const canGetPVC = useSelfSubjectAccessReview({
    resourceAttributes: {
      namespace,
      group: "",
      resource: "persistentvolumeclaims",
      verb: "get",
    },
  })

  const checking =
    cdiConfig.isLoading || canCreateToken.isLoading || canGetPVC.isLoading
  if (checking) {
    return <p className="mt-4 text-sm text-slate-600">Checking upload prerequisites…</p>
  }

  const proxyURL = proxyURLFromCDIConfig(cdiConfig.data)
  const proxyError = cdiConfig.isError
  const permissionError = !!canCreateToken.error || !!canGetPVC.error
  const permissionDenied = !canCreateToken.allowed || !canGetPVC.allowed
  const command =
    !proxyError && !permissionError && !permissionDenied
      ? virtctlUploadCommand({ name, namespace, uploadProxyURL: proxyURL })
      : undefined

  const refresh = () => {
    void cdiConfig.refetch()
    void canCreateToken.refetch()
    void canGetPVC.refetch()
  }
  const refreshing =
    cdiConfig.isFetching || canCreateToken.isFetching || canGetPVC.isFetching

  if (!command) {
    return (
      <div className="mt-4 space-y-2">
        {proxyError && (
          <p className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800">
            The console could not verify the upload proxy. Retry the checks, or ask
            your platform administrator for help.
          </p>
        )}
        {!proxyError && !proxyURL && (
          <p className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800">
            This cluster does not advertise a usable upload proxy URL. Ask your
            platform administrator to expose and configure{" "}
            <span className="font-mono">cdi-uploadproxy</span>.
          </p>
        )}
        {permissionError && (
          <p className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800">
            The console could not verify your upload permissions. Retry the checks,
            or ask your platform administrator to run the upload.
          </p>
        )}
        {!permissionError && permissionDenied && (
          <p className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800">
            Your account does not have all permissions required by virtctl: it must
            read <span className="font-mono">persistentvolumeclaims</span> and create{" "}
            <span className="font-mono">uploadtokenrequests</span> in this namespace.
            Ask your platform administrator to run the upload.
          </p>
        )}
        <Button variant="outline" size="sm" onClick={refresh} disabled={refreshing}>
          <RefreshCw className={refreshing ? "size-3.5 animate-spin" : "size-3.5"} />
          Retry checks
        </Button>
      </div>
    )
  }

  return (
    <div className="mt-4 space-y-2">
      <p className="text-sm text-slate-600">
        Uploading runs from your machine — the browser cannot use the CDI upload
        proxy. Point <span className="font-mono text-xs">--image-path</span> at your
        local image and run the command below. CDI keeps this state while a transfer
        is active, so do not start a second command if virtctl is already uploading.
      </p>
      <CopyableCommand command={command} />
    </div>
  )
}

function currentState(dv: DataVolume | undefined): UploadState {
  if (!dv) return { stage: "preparing", phase: "" }
  return uploadState(dv)
}

function UploadDiskPanel({
  ad,
  instance,
}: {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}) {
  const namespace = instance.metadata.namespace ?? ""
  const name = releasePrefix(ad) + instance.metadata.name
  const ready = readyCondition(instance)
  const canReadDataVolume = ready?.status === "True"
  const dvQuery = useK8sList<DataVolume>(
    {
      apiGroup: CDI_GROUP,
      apiVersion: CDI_VERSION,
      plural: "datavolumes",
      namespace,
    },
    {
      enabled: canReadDataVolume && !!namespace && !!instance.metadata.name,
      fieldSelector: "metadata.name=" + name,
      placeholderData: undefined,
      refetchOnMount: "always",
      retry: requestRetry,
      retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 4000),
    },
  )
  const dv =
    canReadDataVolume && !dvQuery.isError ? dvQuery.data?.items[0] : undefined
  const sourceMismatch = !!dv && !isUploadSource(dv)
  const state: UploadState = dvQuery.isError || sourceMismatch
    ? { stage: "unknown", phase: dv?.status?.phase ?? "" }
    : currentState(dv)
  const capacity = dataVolumeCapacity(dv)

  return (
    <div className="px-6 pt-6">
      <Section
        title="Image upload"
        description="This disk is configured to receive a local image."
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => void dvQuery.refetch()}
            disabled={!canReadDataVolume || dvQuery.isFetching}
          >
            <RefreshCw className="size-3.5" /> Refresh
          </Button>
        }
      >
        <div className="flex items-center gap-2" aria-live="polite">
          <StatusBadge tone={STAGE_TONE[state.stage]}>
            {STAGE_LABEL[state.stage]}
          </StatusBadge>
          {state.phase && (
            <span className="font-mono text-xs text-slate-500">{state.phase}</span>
          )}
          {state.progress && state.progress !== "N/A" && (
            <span className="tabular-nums text-xs text-slate-500">
              {state.progress}
            </span>
          )}
        </div>

        {!canReadDataVolume && (
          <p className="mt-3 text-sm text-slate-600">
            Waiting for disk reconciliation. The upload target will be watched after
            the VMDisk becomes Ready.
          </p>
        )}

        {canReadDataVolume && dvQuery.isError && (
          <p className="mt-3 text-sm text-red-700">
            The console could not read the upload target. Refresh to try again.
          </p>
        )}

        {canReadDataVolume && dvQuery.isLoading && (
          <p className="mt-3 text-sm text-slate-600">Reading the upload target…</p>
        )}

        {canReadDataVolume && dvQuery.isSuccess && !dv && (
          <p className="mt-3 text-sm text-slate-600">
            CDI has not created the upload target yet. This page is watching for it.
          </p>
        )}

        {sourceMismatch && (
          <p className="mt-3 text-sm text-red-700">
            The underlying DataVolume is not configured for upload. Refresh after
            the VMDisk finishes reconciling, or recreate the disk with an upload
            source.
          </p>
        )}

        {!sourceMismatch && dv && state.stage === "preparing" && (
          <p className="mt-3 text-sm text-slate-600">
            CDI is provisioning the upload target. The disk accepts an image once it
            reaches <span className="font-mono text-xs">UploadReady</span>.
          </p>
        )}

        {!sourceMismatch && dv && state.stage === "succeeded" && (
          <p className="mt-3 text-sm text-slate-600">
            The image has been written to this disk. Re-uploading requires recreating
            the disk.
          </p>
        )}

        {!sourceMismatch && dv && state.stage === "paused" && (
          <p className="mt-3 text-sm text-slate-600">
            CDI has paused reconciliation for this upload target. The command is
            withheld until it resumes.
          </p>
        )}

        {!sourceMismatch && dv && state.stage === "failed" && (
          <p className="mt-3 text-sm text-slate-600">
            CDI can no longer accept an upload for this disk. Recreate the disk to
            try again.
          </p>
        )}

        {!sourceMismatch && dv && state.stage === "unknown" && (
          <p className="mt-3 text-sm text-slate-600">
            CDI reported an upload phase the console does not recognise. The command
            is withheld until the disk returns to a known state.
          </p>
        )}

        {state.message && <p className="mt-3 text-sm text-red-700">{state.message}</p>}

        {capacity && (
          <p className="mt-3 text-xs text-slate-500">
            Virtual image capacity:{" "}
            <span className="font-mono text-slate-700">{capacity}</span>. The image's
            virtual size must fit within this disk.
          </p>
        )}

        {!sourceMismatch && dv && state.stage === "awaiting-upload" && (
          <UploadPrerequisites name={name} namespace={namespace} />
        )}
      </Section>
    </div>
  )
}

/** Shows upload state and safe virtctl handoff for upload-source VMDisk resources. */
export function DiskUploadPanel({
  ad,
  instance,
}: {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}) {
  if (!isUploadSource(instance)) return null
  return <UploadDiskPanel ad={ad} instance={instance} />
}
