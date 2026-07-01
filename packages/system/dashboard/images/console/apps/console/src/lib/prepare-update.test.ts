import { describe, it, expect, vi } from "vitest"
import { prepareUpdateSpec } from "./prepare-update.ts"

const immutableStorageClassSchema = JSON.stringify({
  type: "object",
  properties: {
    storageClass: {
      type: "string",
      "x-kubernetes-validations": [
        { rule: "self == oldSelf", message: "immutable" },
      ],
    },
    size: { type: "string" },
  },
})

const schemaWithoutImmutability = JSON.stringify({
  type: "object",
  properties: {
    storageClass: { type: "string" },
    size: { type: "string" },
  },
})

describe("prepareUpdateSpec", () => {
  it("overlays immutable values from initialSpec into submitted spec", () => {
    const submitted = { storageClass: "fast", size: "10Gi" }
    const initial = { storageClass: "slow", size: "5Gi" }
    expect(
      prepareUpdateSpec(submitted, initial, immutableStorageClassSchema),
    ).toEqual({ storageClass: "slow", size: "10Gi" })
  })

  it("returns a clone of submitted unchanged when the schema declares no immutability", () => {
    const submitted = { storageClass: "fast", size: "10Gi" }
    const initial = { storageClass: "slow", size: "5Gi" }
    const result = prepareUpdateSpec(submitted, initial, schemaWithoutImmutability)
    expect(result).toEqual(submitted)
    expect(result).not.toBe(submitted)
  })

  it("returns a clone of submitted unchanged when the schema is malformed JSON", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {})
    const submitted = { storageClass: "fast" }
    const initial = { storageClass: "slow" }
    const result = prepareUpdateSpec(submitted, initial, "not-json")
    expect(result).toEqual(submitted)
    warn.mockRestore()
  })

  it("does not mutate inputs", () => {
    const submitted = { storageClass: "fast", size: "10Gi" }
    const initial = { storageClass: "slow", size: "5Gi" }
    prepareUpdateSpec(submitted, initial, immutableStorageClassSchema)
    expect(submitted).toEqual({ storageClass: "fast", size: "10Gi" })
    expect(initial).toEqual({ storageClass: "slow", size: "5Gi" })
  })

  it("returns submitted unchanged when original is undefined or null", () => {
    const submitted = { storageClass: "fast", size: "10Gi" }
    expect(
      prepareUpdateSpec(submitted, undefined, immutableStorageClassSchema),
    ).toEqual(submitted)
    expect(
      prepareUpdateSpec(submitted, null, immutableStorageClassSchema),
    ).toEqual(submitted)
  })

  it("warns to console when the schema cannot be parsed", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {})
    prepareUpdateSpec({ storageClass: "fast" }, { storageClass: "slow" }, "{")
    expect(warn).toHaveBeenCalled()
    expect(warn.mock.calls[0][0]).toMatch(/openAPISchema/)
    warn.mockRestore()
  })
})
