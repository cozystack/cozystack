import { describe, it, expect } from "vitest"
import { graftOptionSources } from "./crd-option-sources.ts"

interface SchemaNode {
  type?: string
  properties?: Record<string, SchemaNode>
  "x-cozystack-options"?: { source: string }
}

describe("graftOptionSources", () => {
  it("grafts a source onto a nested field (applicationRef.kind)", () => {
    const spec: SchemaNode = {
      type: "object",
      properties: {
        applicationRef: {
          type: "object",
          properties: {
            kind: { type: "string" },
            name: { type: "string" },
          },
        },
      },
    }

    const out = graftOptionSources(spec, {
      "options.cozystack.io/source.applicationRef.kind": "appkind",
    }) as SchemaNode

    expect(out.properties?.applicationRef?.properties?.kind?.["x-cozystack-options"]).toEqual({
      source: "appkind",
    })
    // Sibling left untouched.
    expect(
      out.properties?.applicationRef?.properties?.name?.["x-cozystack-options"],
    ).toBeUndefined()
  })

  it("grafts a source onto a top-level spec field (backupClassName)", () => {
    const spec: SchemaNode = {
      type: "object",
      properties: { backupClassName: { type: "string" } },
    }

    const out = graftOptionSources(spec, {
      "options.cozystack.io/source.backupClassName": "backupclass",
    }) as SchemaNode

    expect(out.properties?.backupClassName?.["x-cozystack-options"]).toEqual({
      source: "backupclass",
    })
  })

  it("applies every source annotation on a CRD (BackupJob shape)", () => {
    const spec: SchemaNode = {
      type: "object",
      properties: {
        applicationRef: { type: "object", properties: { kind: { type: "string" } } },
        planRef: { type: "object", properties: { name: { type: "string" } } },
        backupClassName: { type: "string" },
      },
    }

    const out = graftOptionSources(spec, {
      "controller-gen.kubebuilder.io/version": "v0.16.4",
      "options.cozystack.io/source.applicationRef.kind": "appkind",
      "options.cozystack.io/source.planRef.name": "plan",
      "options.cozystack.io/source.backupClassName": "backupclass",
    }) as SchemaNode

    expect(out.properties?.applicationRef?.properties?.kind?.["x-cozystack-options"]).toEqual({
      source: "appkind",
    })
    expect(out.properties?.planRef?.properties?.name?.["x-cozystack-options"]).toEqual({
      source: "plan",
    })
    expect(out.properties?.backupClassName?.["x-cozystack-options"]).toEqual({
      source: "backupclass",
    })
  })

  it("ignores annotations without the option-source prefix", () => {
    const spec: SchemaNode = {
      type: "object",
      properties: { backupClassName: { type: "string" } },
    }

    const out = graftOptionSources(spec, {
      "controller-gen.kubebuilder.io/version": "v0.16.4",
      "options.cozystack.io/other": "noise",
    }) as SchemaNode

    expect(out.properties?.backupClassName?.["x-cozystack-options"]).toBeUndefined()
  })

  it("is a no-op for a path that does not exist in the schema", () => {
    const spec: SchemaNode = {
      type: "object",
      properties: { backupClassName: { type: "string" } },
    }

    expect(() =>
      graftOptionSources(spec, {
        "options.cozystack.io/source.missing.field": "appkind",
      }),
    ).not.toThrow()
  })

  it("does not mutate the input schema, including nested nodes", () => {
    const spec: SchemaNode = {
      type: "object",
      properties: {
        backupClassName: { type: "string" },
        applicationRef: { type: "object", properties: { kind: { type: "string" } } },
      },
    }

    graftOptionSources(spec, {
      "options.cozystack.io/source.backupClassName": "backupclass",
      "options.cozystack.io/source.applicationRef.kind": "appkind",
    })

    // Both a top-level field and a nested one must be left untouched, so a
    // future shallow/partial clone that corrupts deep nodes can't slip through.
    expect(spec.properties?.backupClassName?.["x-cozystack-options"]).toBeUndefined()
    expect(spec.properties?.applicationRef?.properties?.kind?.["x-cozystack-options"]).toBeUndefined()
  })

  it("returns the schema unchanged when there are no annotations", () => {
    const spec: SchemaNode = {
      type: "object",
      properties: { backupClassName: { type: "string" } },
    }

    expect(graftOptionSources(spec, undefined)).toBe(spec)
    expect(graftOptionSources(spec, {})).toEqual(spec)
  })
})
