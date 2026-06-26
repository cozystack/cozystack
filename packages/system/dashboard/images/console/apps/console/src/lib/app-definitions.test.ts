import { describe, it, expect } from "vitest"
import { releasePrefix } from "./app-definitions.ts"
import type { ApplicationDefinition } from "@cozystack/types"

function ad(overrides: Partial<ApplicationDefinition> = {}): ApplicationDefinition {
  return {
    apiVersion: "cozystack.io/v1alpha1",
    kind: "ApplicationDefinition",
    metadata: { name: "virtual-machine" },
    spec: {
      application: {
        kind: "VMInstance",
        plural: "vminstances",
        singular: "vm-instance",
        openAPISchema: "{}",
      },
    },
    ...overrides,
  }
}

describe("releasePrefix", () => {
  it("returns the explicit release.prefix when set", () => {
    expect(
      releasePrefix(
        ad({
          spec: {
            application: {
              kind: "VMInstance",
              plural: "vminstances",
              singular: "vm-instance",
              openAPISchema: "{}",
            },
            release: { prefix: "custom-" },
          },
        }),
      ),
    ).toBe("custom-")
  })

  it("falls back to '<singular>-' when release.prefix is unset", () => {
    expect(releasePrefix(ad())).toBe("vm-instance-")
  })

  it("falls back to '<metadata.name>-' when neither prefix nor spec is present", () => {
    expect(releasePrefix(ad({ spec: undefined }))).toBe("virtual-machine-")
  })
})
