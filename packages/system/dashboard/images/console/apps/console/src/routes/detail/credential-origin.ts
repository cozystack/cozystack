import type { Credential } from "./use-app-credentials.ts"

export function formatTime(at: string | undefined): string {
  if (!at) return "—"
  const date = new Date(at)
  return Number.isNaN(date.getTime()) ? at : date.toLocaleString(undefined, { timeZoneName: "short" })
}

// The platform's own service accounts seed, migrate and adopt accounts; a
// tenant reads them as one actor rather than as Kubernetes identities.
const platformActorPrefix = "system:serviceaccount:cozy-system:"

export function actorLabel(by: string | undefined): string {
  if (!by) return "unknown"
  return by.startsWith(platformActorPrefix) ? "the platform" : by
}

// The API names the operations after its subresources; the console calls a
// mint "Generate password", so the history uses the same word.
export function operationLabel(operation: string): string {
  switch (operation) {
    case "Create": return "Created"
    case "Migrate": return "Migrated"
    case "Mint": return "Generated"
    case "Revoke": return "Revoked"
    default: return operation
  }
}

export function originLabel(credential: Credential): string {
  const origin = credential.status?.password?.origin
  switch (origin) {
    case "NotIssued": return "Not issued"
    case "Migrated": return "Migrated"
    case "Minted": return "Generated"
    case "Revoked": return "Revoked"
    default: return "Unknown"
  }
}

export function originExplanation(credential: Credential): string {
  const password = credential.status?.password
  switch (password?.origin) {
    case "NotIssued":
      return "Nobody holds a password yet. Generate one to log in."
    case "Migrated":
      return "The password from before the conversion still works. Replace it by generating a new one."
    case "Minted":
      return `Shown once to ${actorLabel(password.by)} at ${formatTime(password.at)}.`
    case "Revoked":
      return "The password was revoked. Nobody holds a working one until a new one is generated."
    default:
      return "The account has no recorded password state yet."
  }
}
