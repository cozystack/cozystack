import type { K8sResource } from "@cozystack/k8s-client"

export interface DataVolumeSpec {
  source?: Record<string, unknown>
  storage?: {
    resources?: {
      requests?: {
        storage?: string
      }
    }
  }
}

export interface DataVolumeCondition {
  type?: string
  status?: string
  reason?: string
  message?: string
}

export interface DataVolumeStatus {
  phase?: string
  progress?: string
  conditions?: DataVolumeCondition[]
}

export type DataVolume = K8sResource<DataVolumeSpec, DataVolumeStatus>

export interface CDIConfigSpec {
  uploadProxyURLOverride?: string
}

export interface CDIConfigStatus {
  uploadProxyURL?: string
}

export type CDIConfig = K8sResource<CDIConfigSpec, CDIConfigStatus>

export type UploadStage =
  | "preparing"
  | "awaiting-upload"
  | "paused"
  | "succeeded"
  | "failed"
  | "unknown"

export interface UploadState {
  stage: UploadStage
  phase: string
  progress?: string
  message?: string
}

interface ResourceWithSpec {
  spec?: unknown
}

const PREPARING_PHASES = new Set([
  "",
  "Pending",
  "PVCBound",
  "WaitForFirstConsumer",
  "PendingPopulation",
  "PrepClaimInProgress",
  "RebindInProgress",
  "ExpansionInProgress",
  "UploadScheduled",
])

// CDI v1.64 can leave a failed upload pod at UploadScheduled. These are
// Kubernetes container termination reasons, unlike waiting reasons such as
// ContainerCreating and ImagePullBackOff.
const TERMINAL_RUNNING_REASONS = new Set([
  "Error",
  "OOMKilled",
  "ContainerCannotRun",
  "StartError",
  "DeadlineExceeded",
])

const QUIET_RUNNING_REASONS = new Set([
  "",
  "PodRunning",
  "ContainerCreating",
  "PodInitializing",
  "Completed",
])

function asRecord(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined
  }
  return value as Record<string, unknown>
}

/** Returns whether a Kubernetes resource declares `spec.source.upload`. */
export function isUploadSource(resource: ResourceWithSpec | undefined): boolean {
  const spec = asRecord(resource?.spec)
  const source = asRecord(spec?.source)
  return !!source && Object.prototype.hasOwnProperty.call(source, "upload")
}

function conditionText(condition: DataVolumeCondition | undefined): string | undefined {
  return condition?.message?.trim() || condition?.reason?.trim() || undefined
}

function failureMessage(dv: DataVolume): string | undefined {
  const running = dv.status?.conditions?.find((condition) => condition.type === "Running")
  const bound = dv.status?.conditions?.find((condition) => condition.type === "Bound")
  return conditionText(running) || conditionText(bound)
}

function runningProblem(dv: DataVolume): string | undefined {
  const running = dv.status?.conditions?.find((condition) => condition.type === "Running")
  if (running?.status !== "False") return undefined
  if (QUIET_RUNNING_REASONS.has(running.reason?.trim() ?? "")) return undefined
  return conditionText(running)
}

function hasTerminalRunningFailure(dv: DataVolume): boolean {
  const running = dv.status?.conditions?.find((condition) => condition.type === "Running")
  return (
    running?.status === "False" &&
    TERMINAL_RUNNING_REASONS.has(running.reason?.trim() ?? "")
  )
}

/** Maps pinned CDI phases and conditions to the upload lifecycle shown in the UI. */
export function uploadState(dv: DataVolume | undefined): UploadState {
  if (!dv) return { stage: "unknown", phase: "" }
  const phase = dv.status?.phase ?? ""
  if (phase === "Succeeded") return { stage: "succeeded", phase }
  if (phase === "Failed" || hasTerminalRunningFailure(dv)) {
    return { stage: "failed", phase, message: failureMessage(dv) }
  }
  if (phase === "Paused") {
    return { stage: "paused", phase, message: runningProblem(dv) }
  }
  if (phase === "UploadReady") {
    return {
      stage: "awaiting-upload",
      phase,
      progress: dv.status?.progress,
      message: runningProblem(dv),
    }
  }
  if (PREPARING_PHASES.has(phase)) {
    return { stage: "preparing", phase, message: runningProblem(dv) }
  }
  return { stage: "unknown", phase, message: runningProblem(dv) }
}

/** Returns the virtual image capacity requested by the DataVolume. */
export function dataVolumeCapacity(dv: DataVolume | undefined): string | undefined {
  return dv?.spec?.storage?.resources?.requests?.storage?.trim() || undefined
}

/** Validates an administrator-provided CDI upload proxy as an HTTP(S) URL. */
export function usableProxyURL(uploadProxyURL: string | undefined): string | undefined {
  const value = uploadProxyURL?.trim()
  if (!value) return undefined
  for (const character of value) {
    const code = character.charCodeAt(0)
    if (code < 0x20 || code === 0x7f) return undefined
  }
  try {
    const parsed = new URL(value)
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") return undefined
    if (!parsed.hostname || parsed.username || parsed.password) return undefined
  } catch {
    return undefined
  }
  return value
}

/** Resolves CDIConfig with the same explicit-override precedence as virtctl. */
export function proxyURLFromCDIConfig(config: CDIConfig | undefined): string | undefined {
  const configured = config?.spec?.uploadProxyURLOverride ?? config?.status?.uploadProxyURL
  return usableProxyURL(configured)
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`
}

export interface UploadCommandOptions {
  name: string
  namespace: string
  uploadProxyURL?: string
}

/** Builds a paste-safe virtctl command, or nothing when no real proxy is known. */
export function virtctlUploadCommand(opts: UploadCommandOptions): string | undefined {
  const proxy = usableProxyURL(opts.uploadProxyURL)
  if (!proxy) return undefined
  return [
    "virtctl image-upload dv",
    shellQuote(opts.name),
    "--no-create",
    "--namespace",
    shellQuote(opts.namespace),
    "--image-path",
    shellQuote("./disk.qcow2"),
    "--uploadproxy-url",
    shellQuote(proxy),
    "--insecure",
  ].join(" ")
}
