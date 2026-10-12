import { describe, it, expect } from "vitest"
import { enrichSchemaWithEnums } from "./backup-utils.ts"

describe("enrichSchemaWithEnums", () => {
  it("returns non-object schemas unchanged", () => {
    expect(enrichSchemaWithEnums(null, [], {})).toBeNull()
    expect(enrichSchemaWithEnums("string", [], {})).toBe("string")
  })

  it("leaves the schema untouched when no path matches the enum map", () => {
    const schema = { type: "string" }
    expect(enrichSchemaWithEnums(schema, ["foo"], { bar: ["a", "b"] })).toEqual({
      type: "string",
    })
  })

  it("attaches enum values at a top-level path", () => {
    const schema = { type: "string" }
    expect(enrichSchemaWithEnums(schema, ["mode"], { mode: ["a", "b"] })).toEqual({
      type: "string",
      enum: ["a", "b"],
    })
  })

  it("attaches enum values at a nested path", () => {
    const schema = {
      type: "object",
      properties: { mode: { type: "string" } },
    }
    const result = enrichSchemaWithEnums(schema, ["spec"], {
      "spec.mode": ["a", "b"],
    })
    expect(result.properties.mode).toEqual({ type: "string", enum: ["a", "b"] })
  })

  it("does not mutate the caller's path array", () => {
    const path = ["spec"]
    enrichSchemaWithEnums(
      { properties: { mode: { type: "string" } } },
      path,
      { "spec.mode": ["a", "b"] },
    )
    expect(path).toEqual(["spec"])
  })
})
