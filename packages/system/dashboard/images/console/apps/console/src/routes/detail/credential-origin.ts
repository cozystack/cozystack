import type { Credential } from "./use-app-credentials.ts"

export function formatTime(at: string | undefined): string {
  if (!at) return "—"
  const date = new Date(at)
  return Number.isNaN(date.getTime()) ? at : date.toLocaleString()
}

export function originLabel(credential: Credential): string {
  const origin = credential.status?.password?.origin
  switch (origin) {
    case "NotIssued": return "Not issued"
    case "Migrated": return "Migrated"
    case "Minted": return "Minted"
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
      return `Shown once to ${password.by || "an unknown user"} at ${formatTime(password.at)}.`
    case "Revoked":
      return "The password was revoked. Nobody holds a working one until a new one is generated."
    default:
      return "The account has no recorded password state yet."
  }
}
