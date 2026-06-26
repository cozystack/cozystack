import { describe, it, expect } from "vitest"
import type { RJSFSchema, UiSchema } from "@rjsf/utils"
import { addDynamicOptionWidgets } from "./dynamic-options.ts"

describe("addDynamicOptionWidgets", () => {
  it("binds a top-level field carrying x-cozystack-options to the widget", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        backupClassName: {
          type: "string",
          "x-cozystack-options": { source: "backupclass" },
        } as RJSFSchema,
        unrelated: { type: "string" },
      },
    }

    const ui = addDynamicOptionWidgets(schema)

    expect(ui.backupClassName?.["ui:widget"]).toBe("DynamicOptionsWidget")
    expect(ui.unrelated).toBeUndefined()
  })

  it("recurses into nested object properties (applicationRef.kind)", () => {
    // Mirrors the backup CRD shape: applicationRef.kind -> appkind source,
    // applicationRef.name left as plain string (served by client enumMap).
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        applicationRef: {
          type: "object",
          properties: {
            kind: {
              type: "string",
              "x-cozystack-options": { source: "appkind" },
            } as RJSFSchema,
            name: { type: "string" },
          },
        },
        backupClassName: {
          type: "string",
          "x-cozystack-options": { source: "backupclass" },
        } as RJSFSchema,
      },
    }

    const ui = addDynamicOptionWidgets(schema)

    expect(ui.applicationRef?.kind?.["ui:widget"]).toBe("DynamicOptionsWidget")
    expect(ui.applicationRef?.name?.["ui:widget"]).toBeUndefined()
    expect(ui.backupClassName?.["ui:widget"]).toBe("DynamicOptionsWidget")
  })

  it("recurses into a nested reference object (planRef.name)", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        planRef: {
          type: "object",
          properties: {
            name: {
              type: "string",
              "x-cozystack-options": { source: "plan" },
            } as RJSFSchema,
          },
        },
      },
    }

    const ui = addDynamicOptionWidgets(schema)

    expect(ui.planRef?.name?.["ui:widget"]).toBe("DynamicOptionsWidget")
  })

  it("recurses into array items (vm-instance disks[].name)", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        disks: {
          type: "array",
          items: {
            type: "object",
            properties: {
              name: {
                type: "string",
                "x-cozystack-options": { source: "vmdisk" },
              } as RJSFSchema,
              size: { type: "string" },
            },
          },
        } as RJSFSchema,
      },
    }

    const ui = addDynamicOptionWidgets(schema)

    expect(ui.disks?.items?.name?.["ui:widget"]).toBe("DynamicOptionsWidget")
    expect(ui.disks?.items?.size?.["ui:widget"]).toBeUndefined()
  })

  it("recurses into additionalProperties maps (kubernetes nodeGroups.*.instanceType)", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        nodeGroups: {
          type: "object",
          additionalProperties: {
            type: "object",
            properties: {
              instanceType: {
                type: "string",
                "x-cozystack-options": { source: "instancetype" },
              } as RJSFSchema,
            },
          },
        } as RJSFSchema,
      },
    }

    const ui = addDynamicOptionWidgets(schema)

    expect(ui.nodeGroups?.additionalProperties?.instanceType?.["ui:widget"]).toBe(
      "DynamicOptionsWidget",
    )
  })

  it("pins the current 'oneOf/anyOf/allOf branches are not walked' limitation", () => {
    // FIXME: extend the walker to recurse into oneOf/anyOf/allOf. Once that
    // lands, flip this to expect the inner field to bind DynamicOptionsWidget.
    // Matches the same intentional gap pinned in sensitive-fields.test.ts.
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        source: {
          oneOf: [
            {
              type: "object",
              properties: {
                storageClass: {
                  type: "string",
                  "x-cozystack-options": { source: "storageclass" },
                } as RJSFSchema,
              },
            },
          ],
        },
      },
    }

    const ui = addDynamicOptionWidgets(schema)

    expect(ui.source).toBeUndefined()
  })

  it("does not mutate the input uiSchema (returns fresh objects at every level)", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        applicationRef: {
          type: "object",
          properties: {
            kind: {
              type: "string",
              "x-cozystack-options": { source: "appkind" },
            } as RJSFSchema,
          },
        },
      },
    }
    const input: UiSchema = { applicationRef: { kind: { "ui:title": "Kind" } } }
    const snapshot = JSON.parse(JSON.stringify(input))

    const out = addDynamicOptionWidgets(schema, input)

    // The input object graph is left byte-identical.
    expect(input).toEqual(snapshot)
    // The result and every touched sub-object are new references.
    expect(out).not.toBe(input)
    expect(out.applicationRef).not.toBe(input.applicationRef)
  })

  it("preserves an existing uiSchema while adding widgets", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        backupClassName: {
          type: "string",
          "x-cozystack-options": { source: "backupclass" },
        } as RJSFSchema,
      },
    }

    const ui = addDynamicOptionWidgets(schema, {
      backupClassName: { "ui:title": "Backup Class" },
    })

    expect(ui.backupClassName?.["ui:widget"]).toBe("DynamicOptionsWidget")
    expect(ui.backupClassName?.["ui:title"]).toBe("Backup Class")
  })
})
