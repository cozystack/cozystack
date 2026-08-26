/**
 * Shared shapes and helpers for the forklift.cozystack.io console pages.
 *
 * The API is a pair split by lifecycle: a VMImportSource is a long-lived
 * connection whose readiness is a condition, and a VMImportTask is a one-shot
 * operation with a phase and per-machine progress. Deleting a task leaves the
 * disks and instances it produced in place.
 */

export const MIGRATION_GROUP = "forklift.cozystack.io"
export const MIGRATION_VERSION = "v1alpha1"

export type MigrationResourceType = "vmimportsources" | "vmimporttasks"

export interface MigrationCondition {
  type: string
  status: string
  reason?: string
  message?: string
}

export interface VMImportVMStatus {
  id: string
  name?: string
  phase?: string
  progress?: number
  vmInstance?: string
  disks?: string[]
  message?: string
}

export interface MigrationResource {
  apiVersion: string
  kind: string
  metadata: {
    name: string
    namespace?: string
    creationTimestamp?: string
  }
  spec?: {
    // VMImportSource
    type?: string
    url?: string
    // VMImportTask
    sourceRef?: { name?: string }
    storageClass?: string
    vms?: Array<{
      id: string
      name?: string
      instanceType?: string
      instanceProfile?: string
    }>
  }
  status?: {
    phase?: string
    message?: string
    startedAt?: string
    completedAt?: string
    vms?: VMImportVMStatus[]
    conditions?: MigrationCondition[]
  }
}

export function readyCondition(resource: MigrationResource): MigrationCondition | undefined {
  return resource.status?.conditions?.find((c) => c.type === "Ready")
}

/**
 * Whole-task progress as the mean over its machines, so a two-machine import
 * with one finished disk reads 50% rather than jumping between per-machine
 * numbers. Returns null when there is nothing to average yet.
 */
export function taskProgress(resource: MigrationResource): number | null {
  const vms = resource.status?.vms
  if (!vms || vms.length === 0) return null
  const total = vms.reduce((sum, vm) => sum + (vm.progress ?? 0), 0)
  return Math.round(total / vms.length)
}

/** Tone for a phase or condition string, shared by the list and detail pages. */
export function phaseTone(phase: string | undefined): "ok" | "error" | "warn" {
  if (phase === "Succeeded" || phase === "Ready" || phase === "True") return "ok"
  if (phase === "Failed" || phase === "False") return "error"
  return "warn"
}
