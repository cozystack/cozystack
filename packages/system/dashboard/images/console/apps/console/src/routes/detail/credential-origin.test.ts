import { describe, expect, it } from "vitest"

import { actorLabel, formatTime, operationLabel, originLabel } from "./credential-origin.ts"
import type { Credential } from "./use-app-credentials.ts"

describe("credential labels", () => {
  it("names a mint the way the button does", () => {
    expect(operationLabel("Mint")).toBe("Generated")
    expect(operationLabel("Migrate")).toBe("Migrated")
    expect(operationLabel("Revoke")).toBe("Revoked")
    expect(originLabel({ status: { password: { origin: "Minted" } } } as Credential)).toBe("Generated")
  })

  it("shows the platform's service accounts as the platform", () => {
    expect(actorLabel("system:serviceaccount:cozy-system:cozystack-migration-hook")).toBe("the platform")
    expect(actorLabel("system:serviceaccount:tenant-foo:ci")).toBe("system:serviceaccount:tenant-foo:ci")
    expect(actorLabel("jane@example.org")).toBe("jane@example.org")
    expect(actorLabel(undefined)).toBe("unknown")
  })

  it("names the time zone", () => {
    expect(formatTime("2026-11-20T12:00:00Z")).toMatch(/[A-Z]{2,}|GMT|UTC/)
  })
})
