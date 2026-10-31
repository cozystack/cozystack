import { describe, it, expect } from "vitest"
import type { TenantNamespace } from "@cozystack/types"
import { realParentNamespace } from "./tenant-tree.ts"

/**
 * A TenantNamespace labels itself with its whole ancestor chain as
 * `tenant.cozystack.io/<ns>` keys, plus a self label. `ancestors` here is the
 * chain excluding self.
 */
function mkTn(name: string, ancestors: string[]): TenantNamespace {
  const labels: Record<string, string> = { [`tenant.cozystack.io/${name}`]: "" }
  for (const a of ancestors) labels[`tenant.cozystack.io/${a}`] = ""
  return { metadata: { name, labels } } as TenantNamespace
}

describe("realParentNamespace", () => {
  it("resolves a direct child of root to tenant-root despite the missing name prefix", () => {
    // Children of root are named `tenant-<name>`, not `tenant-root-<name>`, so
    // no ancestor label is a literal name prefix of theirs — the real parent is
    // still root. This is the case that hid the Edit button for sub-tenants.
    expect(realParentNamespace(mkTn("tenant-k8s2user19549", ["tenant-root"]))).toBe("tenant-root")
  })

  it("resolves a grandchild to its name-prefix parent", () => {
    expect(
      realParentNamespace(mkTn("tenant-acme-dev", ["tenant-root", "tenant-acme"])),
    ).toBe("tenant-acme")
  })

  it("picks the longest prefixing ancestor for a deep descendant", () => {
    expect(
      realParentNamespace(mkTn("tenant-a-b-c", ["tenant-root", "tenant-a", "tenant-a-b"])),
    ).toBe("tenant-a-b")
  })

  it("returns undefined for the hierarchy root (no ancestors)", () => {
    expect(realParentNamespace(mkTn("tenant-root", []))).toBeUndefined()
  })

  it("returns undefined when neither a name-prefix nor root is an ancestor", () => {
    // A bridged node whose real parent is invisible and is not root.
    expect(realParentNamespace(mkTn("tenant-orphan", ["tenant-somewhere"]))).toBeUndefined()
  })
})
