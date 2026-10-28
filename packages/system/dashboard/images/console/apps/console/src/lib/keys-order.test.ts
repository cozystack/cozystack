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

  it("coerces x-kubernetes-int-or-string to [string, integer] and drops anyOf", () => {
    const out = sanitizeSchema({
      "x-kubernetes-int-or-string": true,
      pattern: QUANTITY_PATTERN,
      anyOf: [{ type: "integer" }, { type: "string" }],
    })
    // string first so RJSF picks StringField and keeps typed values as strings;
    // integer still accepted for a stored/default whole number.
    expect(out.type).toEqual(["string", "integer"])
    expect(out.anyOf).toBeUndefined()
    // The pattern survives so junk strings are still rejected.
    expect(out.pattern).toBe(QUANTITY_PATTERN)
  })

  it("int-or-string field accepts an integer and quantity strings, rejects a float and junk", () => {
    // Oracle is ajv via @rjsf/validator-ajv8, matching the chart's own
    // values.schema.json (anyOf integer|string): a JSON float like 0.5 is
    // invalid there and fails the HelmRelease, so the form must not accept it;
    // the fractional value belongs as the string "0.5".
    const schema = sanitizeSchema({
      description: "CPU limit.",
      default: 1,
      "x-kubernetes-int-or-string": true,
      pattern: QUANTITY_PATTERN,
      anyOf: [{ type: "integer" }, { type: "string" }],
    })
    const errors = (v: unknown) =>
      validator.validateFormData(v, schema as never).errors.map((e) => e.message)
    // Stored/default whole number must be accepted — the original bug.
    expect(errors(1)).toEqual([])
    // Fractional and suffixed quantities are valid as strings.
    expect(errors("0.5")).toEqual([])
    expect(errors("500m")).toEqual([])
    expect(errors("1")).toEqual([])
    // A JSON float must be rejected (the chart schema rejects it too).
    expect(errors(0.5).length).toBeGreaterThan(0)
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
