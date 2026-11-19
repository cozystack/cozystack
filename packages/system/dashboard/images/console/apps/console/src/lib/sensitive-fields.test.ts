import { describe, it, expect } from "vitest"
import type { RJSFSchema, UiSchema } from "@rjsf/utils"
import { addSensitiveStringWidgets, isSensitiveFieldName } from "./sensitive-fields.ts"

describe("isSensitiveFieldName", () => {
  it("matches the canonical S3/postgres credential field names", () => {
    expect(isSensitiveFieldName("s3AccessKey")).toBe(true)
    expect(isSensitiveFieldName("s3SecretKey")).toBe(true)
    expect(isSensitiveFieldName("secretAccessKey")).toBe(true)
    expect(isSensitiveFieldName("secretAccessKeyKey")).toBe(true)
    expect(isSensitiveFieldName("accessKey")).toBe(true)
    expect(isSensitiveFieldName("access_key")).toBe(true)
    expect(isSensitiveFieldName("secret_key")).toBe(true)
  })

  it("matches generic password/token/private-key/api-key style names", () => {
    expect(isSensitiveFieldName("password")).toBe(true)
    expect(isSensitiveFieldName("rootPassword")).toBe(true)
    expect(isSensitiveFieldName("passwordConfirmation")).toBe(true)
    expect(isSensitiveFieldName("passwd")).toBe(true)
    expect(isSensitiveFieldName("token")).toBe(true)
    expect(isSensitiveFieldName("bearerToken")).toBe(true)
    expect(isSensitiveFieldName("api_token")).toBe(true)
    expect(isSensitiveFieldName("apiKey")).toBe(true)
    expect(isSensitiveFieldName("api_key")).toBe(true)
    expect(isSensitiveFieldName("privateKey")).toBe(true)
    expect(isSensitiveFieldName("private_key")).toBe(true)
  })

  it("is case-insensitive", () => {
    expect(isSensitiveFieldName("PASSWORD")).toBe(true)
    expect(isSensitiveFieldName("S3SECRETKEY")).toBe(true)
  })

  it("does not match neighbouring/non-secret names", () => {
    expect(isSensitiveFieldName("storageClass")).toBe(false)
    expect(isSensitiveFieldName("backupClassName")).toBe(false)
    expect(isSensitiveFieldName("bucket")).toBe(false)
    expect(isSensitiveFieldName("region")).toBe(false)
    expect(isSensitiveFieldName("publicKey")).toBe(false)
    expect(isSensitiveFieldName("keyName")).toBe(false)
    expect(isSensitiveFieldName("name")).toBe(false)
  })

  it("respects word boundaries to avoid false positives on token/passwd", () => {
    // `token` is matched only as the trailing word.
    expect(isSensitiveFieldName("tokenAudience")).toBe(false)
    expect(isSensitiveFieldName("tokenizer")).toBe(false)
    expect(isSensitiveFieldName("csrfTokenName")).toBe(false)
    // `passwd` likewise — `passwdFile` is a path, not a credential.
    expect(isSensitiveFieldName("passwdFile")).toBe(false)
    expect(isSensitiveFieldName("etcPasswdPath")).toBe(false)
  })
})

describe("addSensitiveStringWidgets", () => {
  it("adds the widget to a top-level sensitive string field", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        s3AccessKey: { type: "string" },
        bucket: { type: "string" },
      },
    }
    const result = addSensitiveStringWidgets(schema, {})
    expect(result.s3AccessKey).toEqual({ "ui:widget": "SensitiveStringWidget" })
    expect(result.bucket).toBeUndefined()
  })

  it("recurses into nested object schemas", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        backup: {
          type: "object",
          properties: {
            s3SecretKey: { type: "string" },
            bucket: { type: "string" },
          },
        },
      },
    }
    const result = addSensitiveStringWidgets(schema, {}) as Record<string, UiSchema>
    expect(result.backup?.s3SecretKey).toEqual({ "ui:widget": "SensitiveStringWidget" })
    expect(result.backup?.bucket).toBeUndefined()
  })

  it("masks individual string items when the array key itself is sensitive", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        apiKey: { type: "array", items: { type: "string" } },
      },
    }
    const result = addSensitiveStringWidgets(schema, {}) as Record<string, UiSchema>
    const itemsUi = (result.apiKey as { items?: UiSchema } | undefined)?.items
    expect(itemsUi?.["ui:widget"]).toBe("SensitiveStringWidget")
  })

  it("leaves array-of-strings unmasked when the array key is not sensitive", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        labels: { type: "array", items: { type: "string" } },
      },
    }
    const result = addSensitiveStringWidgets(schema, {})
    expect(result.labels).toBeUndefined()
  })

  it("recurses into array items expressed as a tuple of schemas", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        rotations: {
          type: "array",
          items: [
            {
              type: "object",
              properties: {
                password: { type: "string" },
              },
            },
          ],
        },
      },
    }
    const result = addSensitiveStringWidgets(schema, {}) as Record<string, UiSchema>
    const itemsUi = (result.rotations as { items?: Record<string, UiSchema> } | undefined)?.items
    expect(itemsUi?.password).toEqual({ "ui:widget": "SensitiveStringWidget" })
  })

  it("recurses into array item objects", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        users: {
          type: "array",
          items: {
            type: "object",
            properties: {
              name: { type: "string" },
              password: { type: "string" },
            },
          },
        },
      },
    }
    const result = addSensitiveStringWidgets(schema, {}) as Record<string, UiSchema>
    const itemsUi = (result.users as { items?: Record<string, UiSchema> } | undefined)?.items
    expect(itemsUi?.password).toEqual({ "ui:widget": "SensitiveStringWidget" })
    expect(itemsUi?.name).toBeUndefined()
  })

  it("ignores non-string fields even with sensitive names", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        accessKey: { type: "integer" },
        password: { type: "boolean" },
      },
    }
    const result = addSensitiveStringWidgets(schema, {})
    expect(result.accessKey).toBeUndefined()
    expect(result.password).toBeUndefined()
  })

  it("ignores string fields with an enum (rendered as select)", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        password: { type: "string", enum: ["weak", "strong"] },
      },
    }
    const result = addSensitiveStringWidgets(schema, {})
    expect(result.password).toBeUndefined()
  })

  it("does not overwrite an existing ui:widget on the same field", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        accessKey: { type: "string" },
      },
    }
    const initial: UiSchema = {
      accessKey: { "ui:widget": "SomeOtherWidget" },
    }
    const result = addSensitiveStringWidgets(schema, initial)
    expect(result.accessKey).toEqual({ "ui:widget": "SomeOtherWidget" })
  })

  it("preserves unrelated uiSchema entries while adding the widget", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        s3AccessKey: { type: "string" },
        bucket: { type: "string" },
      },
    }
    const initial: UiSchema = {
      bucket: { "ui:placeholder": "my-bucket" },
    }
    const result = addSensitiveStringWidgets(schema, initial)
    expect(result.s3AccessKey).toEqual({ "ui:widget": "SensitiveStringWidget" })
    expect(result.bucket).toEqual({ "ui:placeholder": "my-bucket" })
  })

  it("returns the input uiSchema untouched when the schema has no properties", () => {
    const initial: UiSchema = { "ui:order": ["a", "b"] }
    expect(addSensitiveStringWidgets({} as RJSFSchema, initial)).toEqual(initial)
  })

  it("does not mutate the input uiSchema when array items propagate the widget", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        apiKey: { type: "array", items: { type: "string" } },
      },
    }
    const initial: UiSchema = { apiKey: { items: { "ui:placeholder": "foo" } } }
    const snapshot: UiSchema = JSON.parse(JSON.stringify(initial))
    addSensitiveStringWidgets(schema, initial)
    expect(initial).toEqual(snapshot)
  })

  it("does not mutate the input uiSchema when array items contain a sensitive object field", () => {
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        users: {
          type: "array",
          items: {
            type: "object",
            properties: {
              name: { type: "string" },
              password: { type: "string" },
            },
          },
        },
      },
    }
    const initial: UiSchema = {
      users: { items: { name: { "ui:placeholder": "alice" } } },
    }
    const snapshot: UiSchema = JSON.parse(JSON.stringify(initial))
    addSensitiveStringWidgets(schema, initial)
    expect(initial).toEqual(snapshot)
  })

  it("pins the current 'oneOf branches are not walked' limitation", () => {
    // FIXME: extend addSensitiveStringWidgets to recurse into oneOf/anyOf/allOf.
    // Once that is implemented, flip this test to expect the inner field to
    // be masked instead of being left untouched.
    const schema: RJSFSchema = {
      type: "object",
      properties: {
        auth: {
          oneOf: [
            {
              type: "object",
              properties: { password: { type: "string" } },
            },
          ],
        },
      },
    } as RJSFSchema
    const result = addSensitiveStringWidgets(schema, {})
    expect(result.auth).toBeUndefined()
  })
})
