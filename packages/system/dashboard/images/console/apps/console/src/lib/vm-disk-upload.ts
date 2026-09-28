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

// CDI can leave a stopped upload container at UploadScheduled. These reasons
// describe the container's last failure, not a permanent disk failure: upload
// pods use RestartPolicyOnFailure.
const FAILED_RUNNING_REASONS = new Set([
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

export function isUploadSource(resource: ResourceWithSpec | undefined): boolean {
  const spec = asRecord(resource?.spec)
  const source = asRecord(spec?.source)
  return !!source && Object.prototype.hasOwnProperty.call(source, "upload")
}

function conditionText(condition: DataVolumeCondition | undefined): string | undefined {
  return condition?.message?.trim() || condition?.reason?.trim() || undefined
}

function failureMessage(dv: DataVolume): string | undefined {
  const running = dv.status?.conditions?.find(
    (condition) => condition.type === "Running" && condition.status === "False",
  )
  const bound = dv.status?.conditions?.find(
    (condition) => condition.type === "Bound" && condition.status === "False",
  )
  return conditionText(running) || conditionText(bound)
}

function runningProblem(dv: DataVolume): string | undefined {
  const running = dv.status?.conditions?.find((condition) => condition.type === "Running")
  if (running?.status !== "False") return undefined
  if (QUIET_RUNNING_REASONS.has(running.reason?.trim() ?? "")) return undefined
  return conditionText(running)
}

function hasRunningFailure(dv: DataVolume): boolean {
  const running = dv.status?.conditions?.find((condition) => condition.type === "Running")
  return (
    running?.status === "False" &&
    FAILED_RUNNING_REASONS.has(running.reason?.trim() ?? "")
  )
}

export function uploadState(dv: DataVolume | undefined): UploadState {
  if (!dv) return { stage: "unknown", phase: "" }
  const phase = dv.status?.phase ?? ""
  if (phase === "Succeeded") return { stage: "succeeded", phase }
  if (phase === "Failed" || hasRunningFailure(dv)) {
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

export function dataVolumeCapacity(dv: DataVolume | undefined): string | undefined {
  return dv?.spec?.storage?.resources?.requests?.storage?.trim() || undefined
}

const EXPLICIT_SCHEME = /^[a-z][a-z0-9+.-]*:\/\//i

/** Checks HTTPS URL syntax; reachability and certificate trust belong to the upload client. */
export function usableProxyURL(uploadProxyURL: string | undefined): string | undefined {
  const trimmed = uploadProxyURL?.trim()
  if (!trimmed) return undefined
  for (const character of trimmed) {
    const code = character.charCodeAt(0)
    if (code < 0x20 || code === 0x7f) return undefined
  }
  // virtctl prepends https:// when the value carries no scheme, so a bare host is a valid override.
  const value = EXPLICIT_SCHEME.test(trimmed) ? trimmed : `https://${trimmed}`
  try {
    const parsed = new URL(value)
    if (parsed.protocol !== "https:") return undefined
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

// POSIX single quotes: cmd.exe and PowerShell do not unquote them the same way.
function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`
}

export interface UploadCommandOptions {
  name: string
  namespace: string
  uploadProxyURL?: string
}

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
  ].join(" ")
}
