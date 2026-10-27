import { describe, it, expect } from "vitest"
import validator from "@rjsf/validator-ajv8"
import { keysOrderToUiSchema, sanitizeSchema } from "./keys-order.ts"

// The quantity pattern cozystack emits for int-or-string resource fields.
const QUANTITY_PATTERN =
  "^(\\+|-)?(([0-9]+(\\.[0-9]*)?)|(\\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|([eE](\\+|-)?(([0-9]+(\\.[0-9]*)?)|(\\.[0-9]+))))?$"

describe("keysOrderToUiSchema", () => {
  it("returns empty object for missing/empty input", () => {
    expect(keysOrderToUiSchema(undefined)).toEqual({})
    expect(keysOrderToUiSchema([])).toEqual({})
  })

  it("ignores meta paths (apiVersion, kind, metadata) and only emits spec keys", () => {
    const ui = keysOrderToUiSchema([
      ["apiVersion"],
      ["kind"],
      ["metadata"],
      ["metadata", "name"],
      ["spec", "storageClass"],
      ["spec", "size"],
    ])
    expect(ui).toEqual({ "ui:order": ["storageClass", "size", "*"] })
  })

  it("nests ui:order per parent path", () => {
    const ui = keysOrderToUiSchema([
      ["spec", "nodeGroups"],
      ["spec", "nodeGroups", "md0"],
      ["spec", "nodeGroups", "md0", "minReplicas"],
      ["spec", "nodeGroups", "md0", "maxReplicas"],
    ])
    expect(ui).toEqual({
      "ui:order": ["nodeGroups", "*"],
      nodeGroups: {
        "ui:order": ["md0", "*"],
        md0: {
          "ui:order": ["minReplicas", "maxReplicas", "*"],
        },
      },
    })
  })
})

describe("sanitizeSchema", () => {
  it("returns scalars and null unchanged", () => {
    expect(sanitizeSchema(null)).toBe(null)
    expect(sanitizeSchema(42)).toBe(42)
    expect(sanitizeSchema("x")).toBe("x")
  })

  it("recurses into arrays", () => {
    const input = [{ type: "string" }, { type: "integer" }]
    expect(sanitizeSchema(input)).toEqual(input)
  })

  it("coerces x-kubernetes-int-or-string to [integer, string] and drops anyOf", () => {
    const out = sanitizeSchema({
      "x-kubernetes-int-or-string": true,
      pattern: QUANTITY_PATTERN,
      anyOf: [{ type: "integer" }, { type: "string" }],
    })
    expect(out.type).toEqual(["integer", "string"])
    expect(out.anyOf).toBeUndefined()
    // The pattern survives so junk strings are still rejected.
    expect(out.pattern).toBe(QUANTITY_PATTERN)
  })

  it("int-or-string field validates a bare number and a quantity string, rejects junk", () => {
    // Oracle is ajv via @rjsf/validator-ajv8, not hand-rolled assertions: the
    // sanitised schema is what the form actually validates against at submit.
    const schema = sanitizeSchema({
      description: "CPU limit.",
      default: 1,
      "x-kubernetes-int-or-string": true,
      pattern: QUANTITY_PATTERN,
      anyOf: [{ type: "integer" }, { type: "string" }],
    })
    const errors = (v: unknown) =>
      validator.validateFormData(v, schema as never).errors.map((e) => e.message)
    // Bare integer (the field's own default) must be accepted — this is the bug.
    expect(errors(1)).toEqual([])
    // Suffixed quantity string stays valid.
    expect(errors("500m")).toEqual([])
    expect(errors("1")).toEqual([])
    // Nonsense is still rejected by the pattern.
    expect(errors("bad!!").length).toBeGreaterThan(0)
  })

  it("coerces x-kubernetes-preserve-unknown-fields on a propertyless object", () => {
    const out = sanitizeSchema({
      "x-kubernetes-preserve-unknown-fields": true,
    })
    expect(out.type).toBe("object")
    expect(out.additionalProperties).toBe(true)
  })

  it("rewrites \"Chart Values\" title to \"Parameters\"", () => {
    expect(sanitizeSchema({ title: "Chart Values" }).title).toBe("Parameters")
    expect(sanitizeSchema({ title: "Other" }).title).toBe("Other")
  })

  it("strips x-kubernetes-validations from any level", () => {
    const input = {
      properties: {
        storageClass: {
          type: "string",
          "x-kubernetes-validations": [
            { rule: "self == oldSelf", message: "immutable" },
          ],
        },
        nested: {
          properties: {
            inner: {
              type: "integer",
              "x-kubernetes-validations": [{ rule: "self > 0" }],
            },
          },
        },
      },
    }
    const out = sanitizeSchema(input)
    expect(out.properties.storageClass["x-kubernetes-validations"]).toBeUndefined()
    expect(
      out.properties.nested.properties.inner["x-kubernetes-validations"],
    ).toBeUndefined()
    expect(out.properties.storageClass.type).toBe("string")
  })
})
