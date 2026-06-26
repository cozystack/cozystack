import { createRef } from "react"
import { describe, it, expect } from "vitest"
import { render, screen, act } from "@testing-library/react"
import { SchemaForm, type SchemaFormHandle } from "./SchemaForm.tsx"
import { IMMUTABLE_HELP_TEXT } from "../lib/immutable-paths.ts"

const schema = JSON.stringify({
  type: "object",
  properties: {
    version: {
      type: "string",
      "x-kubernetes-validations": [
        { rule: "self == oldSelf", message: "version is immutable" },
      ],
    },
    description: {
      type: "string",
    },
  },
})

const noop = () => {}

describe("SchemaForm immutableMode", () => {
  it("renders fields editable and without help text when immutableMode is omitted", () => {
    render(
      <SchemaForm
        openAPISchema={schema}
        formData={{ version: "1.0", description: "hi" }}
        onChange={noop}
      />,
    )
    const versionInput = screen.getByLabelText("version") as HTMLInputElement
    expect(versionInput).not.toBeDisabled()
    expect(screen.queryByText(IMMUTABLE_HELP_TEXT)).not.toBeInTheDocument()
  })

  it("greys out immutable fields and shows help text when immutableMode is enforce", () => {
    render(
      <SchemaForm
        openAPISchema={schema}
        formData={{ version: "1.0", description: "hi" }}
        onChange={noop}
        immutableMode="enforce"
      />,
    )
    const versionInput = screen.getByLabelText("version") as HTMLInputElement
    expect(versionInput).toBeDisabled()
    const descriptionInput = screen.getByLabelText(
      "description",
    ) as HTMLInputElement
    expect(descriptionInput).not.toBeDisabled()
    expect(screen.getByText(IMMUTABLE_HELP_TEXT)).toBeInTheDocument()
  })

  it("treats immutableMode='off' the same as omitting the prop", () => {
    render(
      <SchemaForm
        openAPISchema={schema}
        formData={{ version: "1.0", description: "hi" }}
        onChange={noop}
        immutableMode="off"
      />,
    )
    const versionInput = screen.getByLabelText("version") as HTMLInputElement
    expect(versionInput).not.toBeDisabled()
    expect(screen.queryByText(IMMUTABLE_HELP_TEXT)).not.toBeInTheDocument()
  })

  it("greys out every element of a whole-array immutable field", () => {
    // *-as-last: the items schema itself carries the rule, so the entire
    // array is immutable. RJSF receives ui:disabled on `items` and
    // disables each element's input.
    const wholeArraySchema = JSON.stringify({
      type: "object",
      properties: {
        tags: {
          type: "array",
          items: {
            type: "string",
            "x-kubernetes-validations": [
              { rule: "self == oldSelf", message: "tags is immutable" },
            ],
          },
        },
      },
    })
    render(
      <SchemaForm
        openAPISchema={wholeArraySchema}
        formData={{ tags: ["alpha", "beta"] }}
        onChange={noop}
        immutableMode="enforce"
      />,
    )
    expect(screen.getByDisplayValue("alpha")).toBeDisabled()
    expect(screen.getByDisplayValue("beta")).toBeDisabled()
    expect(screen.getAllByText(IMMUTABLE_HELP_TEXT).length).toBeGreaterThan(0)
    // Mirroring the AdditionalPropertiesField map case: the array
    // wrapper is disabled, so RJSF disables Add (and per-element
    // Remove) too. Otherwise the user could append an entry that the
    // overlay would silently drop on save.
    expect(screen.getByRole("button", { name: /add/i })).toBeDisabled()
  })

  it("binds the key/value editor to an additionalProperties map nested in array items", () => {
    // spec.strategies[].parameters shape: an array of objects each carrying a
    // free-form string map. Native rendering shows no Add control here (the
    // custom ObjectFieldTemplate drops it), so an empty map would be
    // uneditable — the walker must reach into items and attach the field.
    const arrayMapSchema = JSON.stringify({
      type: "object",
      properties: {
        strategies: {
          type: "array",
          items: {
            type: "object",
            properties: {
              parameters: {
                type: "object",
                additionalProperties: { type: "string" },
              },
            },
          },
        },
      },
    })
    render(
      <SchemaForm
        openAPISchema={arrayMapSchema}
        formData={{ strategies: [{ parameters: {} }] }}
        onChange={noop}
      />,
    )
    // AdditionalPropertiesField exposes an explicit add-key input even when the
    // map is empty; native additionalProperties rendering would not.
    expect(screen.getByPlaceholderText("Enter key name...")).toBeInTheDocument()
  })

  it("greys out immutable nested fields inside array items", () => {
    const arraySchema = JSON.stringify({
      type: "object",
      properties: {
        volumes: {
          type: "array",
          items: {
            type: "object",
            properties: {
              name: {
                type: "string",
                "x-kubernetes-validations": [
                  { rule: "self == oldSelf", message: "name immutable" },
                ],
              },
              size: { type: "string" },
            },
          },
        },
      },
    })
    render(
      <SchemaForm
        openAPISchema={arraySchema}
        formData={{ volumes: [{ name: "disk1", size: "10Gi" }] }}
        onChange={noop}
        immutableMode="enforce"
      />,
    )
    const nameInput = screen.getByDisplayValue("disk1") as HTMLInputElement
    const sizeInput = screen.getByDisplayValue("10Gi") as HTMLInputElement
    expect(nameInput).toBeDisabled()
    expect(sizeInput).not.toBeDisabled()
    expect(screen.getByText(IMMUTABLE_HELP_TEXT)).toBeInTheDocument()
  })
})

describe("SchemaForm validate handle", () => {
  const requiredSchema = JSON.stringify({
    type: "object",
    required: ["name"],
    properties: { name: { type: "string" }, note: { type: "string" } },
  })

  function validate(ref: React.RefObject<SchemaFormHandle | null>): boolean | undefined {
    let result: boolean | undefined
    act(() => {
      result = ref.current?.validate()
    })
    return result
  }

  it("reports invalid through the ref when a required field is missing", () => {
    const ref = createRef<SchemaFormHandle>()
    render(
      <SchemaForm ref={ref} openAPISchema={requiredSchema} formData={{}} onChange={noop} />,
    )

    expect(validate(ref)).toBe(false)
  })

  it("reports valid through the ref once the required field is populated", () => {
    const ref = createRef<SchemaFormHandle>()
    render(
      <SchemaForm
        ref={ref}
        openAPISchema={requiredSchema}
        formData={{ name: "demo-disk" }}
        onChange={noop}
      />,
    )

    expect(validate(ref)).toBe(true)
  })
})
