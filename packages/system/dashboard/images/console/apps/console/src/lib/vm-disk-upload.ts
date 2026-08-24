import type { K8sResource } from "@cozystack/k8s-client"

export interface DataVolumeSpec {
  source?: Record<string, unknown>
}

export interface DataVolumeStatus {
  phase?: string
  progress?: string
  conditions?: {
    type?: string
    status?: string
    reason?: string
    message?: string
  }[]
}

export type DataVolume = K8sResource<DataVolumeSpec, DataVolumeStatus>

export interface CDIConfigStatus {
  uploadProxyURL?: string
}

export type CDIConfig = K8sResource<unknown, CDIConfigStatus>

/**
 * Where an upload-source disk sits in the CDI lifecycle.
 *
 * `awaiting-upload` is the only stage in which the CDI upload server exists and
 * accepts data — a token minted at any other stage has nothing to talk to.
 */
export type UploadStage =
  | "preparing"
  | "awaiting-upload"
  | "succeeded"
  | "failed"
  | "unknown"

export interface UploadState {
  stage: UploadStage
  phase: string
  progress?: string
  message?: string
}

// CDI DataVolumePhase values that precede the upload server being reachable.
const PREPARING_PHASES = new Set([
  "",
  "Pending",
  "PVCBound",
  "WaitForFirstConsumer",
  "PendingPopulation",
  "UploadScheduled",
])

export function isUploadSource(dv: DataVolume | undefined): boolean {
  const source = dv?.spec?.source
  return !!source && Object.prototype.hasOwnProperty.call(source, "upload")
}

function failureMessage(dv: DataVolume): string | undefined {
  const running = dv.status?.conditions?.find((c) => c.type === "Running")
  const bound = dv.status?.conditions?.find((c) => c.type === "Bound")
  // `||`, not `??`: an empty Running message must not shadow the Bound one.
  return running?.message || bound?.message
}

export function uploadState(dv: DataVolume | undefined): UploadState {
  if (!dv) return { stage: "unknown", phase: "" }
  const phase = dv.status?.phase ?? ""
  if (phase === "Succeeded") return { stage: "succeeded", phase }
  if (phase === "Failed") {
    return { stage: "failed", phase, message: failureMessage(dv) }
  }
  if (phase === "UploadReady") {
    return { stage: "awaiting-upload", phase, progress: dv.status?.progress }
  }
  if (PREPARING_PHASES.has(phase)) return { stage: "preparing", phase }
  return { stage: "unknown", phase }
}

export interface UploadCommandOptions {
  name: string
  namespace: string
  /** CDIConfig.status.uploadProxyURL — absent when the platform published none. */
  uploadProxyURL?: string
}

export const UPLOAD_PROXY_URL_PLACEHOLDER = "https://cdi-uploadproxy.<your-cozystack-domain>"

/**
 * A whitespace-only `CDIConfig.status.uploadProxyURL` is as unusable as an
 * absent one. The command builder and the panel's placeholder warning both
 * read the URL through this, so they cannot disagree over which values need
 * the placeholder.
 */
export function usableProxyURL(uploadProxyURL: string | undefined): string | undefined {
  return uploadProxyURL?.trim() || undefined
}

/**
 * The disk's DataVolume is created by the vm-disk Helm release, so the upload
 * has to reuse it (`--no-create`); `--insecure` is required because Cozystack
 * publishes cdi-uploadproxy through TLS passthrough, leaving CDI's internal
 * self-signed certificate on the wire.
 */
export function virtctlUploadCommand(opts: UploadCommandOptions): string {
  const proxy = usableProxyURL(opts.uploadProxyURL) ?? UPLOAD_PROXY_URL_PLACEHOLDER
  return [
    "virtctl image-upload dv",
    opts.name,
    "--no-create",
    `--namespace=${opts.namespace}`,
    "--image-path=./disk.qcow2",
    `--uploadproxy-url=${proxy}`,
    "--insecure",
  ].join(" ")
}
