import { describe, it, expect } from "vitest"
import type { TenantNamespace } from "@cozystack/types"
import { realParentNamespace } from "./tenant-tree.ts"

function tn(name: string, ancestors: string[]): TenantNamespace {
  const labels: Record<string, string> = {}
  for (const a of [...ancestors, name]) labels[`tenant.cozystack.io/${a}`] = ""
  return {
    apiVersion: "core.cozystack.io/v1alpha1",
    kind: "TenantNamespace",
    metadata: { name, labels },
  } as TenantNamespace
}

describe("realParentNamespace", () => {
  it("takes the deepest ancestor that prefixes the namespace", () => {
    // The full chain is on the labels; only the longest prefix is the parent.
    expect(
      realParentNamespace(
        tn("tenant-acme-eu-dev", ["tenant-root", "tenant-acme", "tenant-acme-eu"]),
      ),
    ).toBe("tenant-acme-eu")
  })

  it("names an inaccessible parent, which is the point of asking", () => {
    // Visibility is the caller's problem: the CR of a bridged row lives in a
    // namespace the user may not read, and the caller has to be able to tell.
    expect(
      realParentNamespace(tn("tenant-acme-eu", ["tenant-root", "tenant-acme"])),
    ).toBe("tenant-acme")
  })

  it("resolves a tenant ordered directly in the root", () => {
    // A tenant `acme` created in `tenant-root` owns `tenant-acme`, not
    // `tenant-root-acme`, so the prefix rule alone leaves every top-level
    // tenant — most tenants on a normal cluster — looking parentless.
    expect(realParentNamespace(tn("tenant-acme", ["tenant-root"]))).toBe(
      "tenant-root",
    )
  })

  it("does not mistake a root-level name that starts with root", () => {
    // `tenant-root-x` is the tenant `root-x` ordered in the root, and the
    // prefix branch answers with the same parent the label does.
    expect(realParentNamespace(tn("tenant-root-x", ["tenant-root"]))).toBe(
      "tenant-root",
    )
  })

  it("has no parent for the hierarchy root", () => {
    expect(realParentNamespace(tn("tenant-root", []))).toBeUndefined()
  })

  it("has no parent when the ancestor labels are gone", () => {
    // A TenantNamespace carrying only its own label is either the root or
    // mislabelled; either way there is no CR namespace to derive.
    expect(realParentNamespace(tn("tenant-acme", []))).toBeUndefined()
  })
})
