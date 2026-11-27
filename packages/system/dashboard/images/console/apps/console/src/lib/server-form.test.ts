import { describe, it, expect } from "vitest"
import { withoutSchemaDefaults } from "./server-form.ts"

describe("server form schema", () => {
  it("removes defaults at every form depth while preserving properties and the original schema", () => {
    const leaf = { type: "string", default: "client" }
    const original = { type: "object", default: {}, properties: {
      default: leaf,
      rows: { type: "array", items: leaf },
      map: { type: "object", additionalProperties: leaf },
    } }
    expect(withoutSchemaDefaults(original)).toEqual({ type: "object", properties: {
      default: { type: "string" },
      rows: { type: "array", items: { type: "string" } },
      map: { type: "object", additionalProperties: { type: "string" } },
    } })
    expect(leaf.default).toBe("client")
    expect(original.default).toEqual({})
  })

  it("does not require a field that has a default, because the server fills it", () => {
    const original = { type: "object", required: ["enabled", "name"], properties: {
      enabled: { type: "boolean", default: false },
      name: { type: "string" },
      nested: { type: "object", required: ["size"], properties: { size: { type: "integer", default: 1 } } },
    } }
    expect(withoutSchemaDefaults(original)).toEqual({ type: "object", required: ["name"], properties: {
      enabled: { type: "boolean" },
      name: { type: "string" },
      nested: { type: "object", properties: { size: { type: "integer" } } },
    } })
    expect(original.required).toEqual(["enabled", "name"])
  })

  it("keeps the requirements of array rows and dictionary values, which the server does not complete", () => {
    const row = { type: "object", required: ["storage", "name"], properties: {
      storage: { type: "string", default: "10Gi" },
      name: { type: "string" },
    } }
    const kept = { type: "object", required: ["storage", "name"], properties: {
      storage: { type: "string" },
      name: { type: "string" },
    } }
    expect(withoutSchemaDefaults({ type: "object", properties: {
      rows: { type: "array", items: row },
      map: { type: "object", additionalProperties: row },
      nested: { type: "object", properties: { rows: { type: "array", items: row } } },
    } })).toEqual({ type: "object", properties: {
      rows: { type: "array", items: kept },
      map: { type: "object", additionalProperties: kept },
      nested: { type: "object", properties: { rows: { type: "array", items: kept } } },
    } })
  })
})
