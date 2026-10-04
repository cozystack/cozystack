import { createRef, useState } from "react"
import { describe, it, expect, vi } from "vitest"
import { act, fireEvent, render, screen } from "@testing-library/react"
import type { FieldProps } from "@rjsf/utils"
import { SchemaForm, type SchemaFormHandle } from "./SchemaForm.tsx"
import { FreeformObjectField } from "./FreeformObjectField.tsx"
import { IMMUTABLE_HELP_TEXT } from "../lib/immutable-paths.ts"

const schema = {
  type: "object",
  properties: {
    users: { type: "object", additionalProperties: { type: "object", properties: { password: { type: "string" } } } },
    config: { type: "object", properties: {
      merge: { type: "object", default: {}, "x-kubernetes-preserve-unknown-fields": true },
      resolver: { type: "object", additionalProperties: true },
    } },
  },
}

function setup(initial: unknown = { users: {}, config: { merge: {}, resolver: {} } }, openAPISchema: unknown = schema, immutableMode?: "enforce") {
  const ref = createRef<SchemaFormHandle>()
  const changed = vi.fn()
  function Controlled() {
    const [data, setData] = useState(initial)
    return <SchemaForm ref={ref} openAPISchema={JSON.stringify(openAPISchema)} formData={data} onChange={(next) => { changed(next); setData(next) }} immutableMode={immutableMode} />
  }
  render(<Controlled />)
  const validate = () => {
    let valid = false
    act(() => { valid = ref.current!.validate() })
    return valid
  }
  return { changed, validate }
}

describe("free-form object editing", () => {
  it("preserves typed whitespace instead of reformatting each valid keystroke", () => {
    const { changed, validate } = setup()
    const text = '{ "jetstream": true, "subjects": ["one"] }\n'
    fireEvent.change(screen.getByLabelText("merge"), { target: { value: text } })
    expect(screen.getByLabelText("merge")).toHaveValue(text)
    expect(changed.mock.lastCall![0].config.merge).toEqual({ jetstream: true, subjects: ["one"] })
    expect(validate()).toBe(true)
  })

  it("edits both NATS objects and preserves nested types and the typed users editor", () => {
    const { changed, validate } = setup()
    const merge = { accounts: { A: { jetstream: { max_streams: 2 }, enabled: false, subjects: ["a", "b"], extra: null } } }
    fireEvent.change(screen.getByLabelText("merge"), { target: { value: JSON.stringify(merge) } })
    fireEvent.change(screen.getByLabelText("resolver"), { target: { value: '{"type":"full","dir":"/data"}' } })
    expect(changed.mock.lastCall![0].config).toEqual({ merge, resolver: { type: "full", dir: "/data" } })
    expect(screen.getByPlaceholderText("Enter key name...")).toBeInTheDocument()
    expect(validate()).toBe(true)
  })

  it.each(["{", "", "[]", "null", "12", '"text"'])("blocks submission for %j and recovers after correction", (input) => {
    const { validate } = setup()
    fireEvent.change(screen.getByLabelText("merge"), { target: { value: input } })
    expect(screen.getByLabelText("merge")).toHaveValue(input)
    expect(screen.getByText("Enter a valid JSON object.")).toBeInTheDocument()
    expect(validate()).toBe(false)
    fireEvent.change(screen.getByLabelText("merge"), { target: { value: '{"jetstream":true}' } })
    expect(validate()).toBe(true)
  })

  it("renders existing values and allows replacing them with an empty object", () => {
    const { changed, validate } = setup({ config: { merge: { accounts: { A: { jetstream: true } } }, resolver: {} } })
    expect(JSON.parse((screen.getByLabelText("merge") as HTMLTextAreaElement).value)).toEqual({ accounts: { A: { jetstream: true } } })
    fireEvent.change(screen.getByLabelText("merge"), { target: { value: "{}" } })
    expect(changed.mock.lastCall![0].config.merge).toEqual({})
    expect(validate()).toBe(true)
  })

  it("keeps immutable objects disabled with their explanation", () => {
    setup({ settings: { enabled: true } }, { type: "object", properties: { settings: { type: "object", additionalProperties: true, "x-kubernetes-validations": [{ rule: "self == oldSelf" }] } } }, "enforce")
    expect(screen.getByLabelText("settings")).toBeDisabled()
    expect(screen.getByText(IMMUTABLE_HELP_TEXT)).toBeInTheDocument()
  })

  it("marks a valid JSON object that fails the schema as invalid and points at its errors", () => {
    const { validate } = setup({ settings: {} }, { type: "object", properties: { settings: { type: "object", additionalProperties: true, minProperties: 1 } } })
    const field = screen.getByLabelText("settings")
    expect(field).toHaveAttribute("aria-invalid", "false")
    expect(validate()).toBe(false)
    expect(field).toHaveAttribute("aria-invalid", "true")
    expect(field.getAttribute("aria-describedby")!.split(" ")).toContain("root_settings__error")
    expect(document.getElementById("root_settings__error")).toBeInTheDocument()
    expect(screen.queryByText("Enter a valid JSON object.")).not.toBeInTheDocument()
    fireEvent.change(field, { target: { value: '{"enabled":true}' } })
    expect(validate()).toBe(true)
    expect(field).toHaveAttribute("aria-invalid", "false")
    expect(field.getAttribute("aria-describedby")!.split(" ")).not.toContain("root_settings__error")
  })

  it("does not point at an error list that hideError keeps from rendering", () => {
    const props = {
      schema: { type: "object", additionalProperties: true },
      formData: {},
      onChange: vi.fn(),
      idSchema: { $id: "root_settings" },
      name: "settings",
      required: false,
      readonly: false,
      disabled: false,
      uiSchema: {},
      rawErrors: ["must NOT have fewer than 1 properties"],
      hideError: true,
      registry: {},
    } as unknown as FieldProps
    render(<FreeformObjectField {...props} />)
    const field = screen.getByLabelText("settings")
    expect(field).toHaveAttribute("aria-invalid", "true")
    expect(field.getAttribute("aria-describedby")!.split(" ")).not.toContain("root_settings__error")
  })

  it("reaches free-form objects nested in arrays", () => {
    const { changed, validate } = setup({ items: [{ options: {} }] }, { type: "object", properties: { items: { type: "array", items: { type: "object", properties: { options: { type: "object", additionalProperties: true } } } } } })
    fireEvent.change(screen.getByLabelText("options"), { target: { value: '{"nested":[1,true]}' } })
    expect(changed.mock.lastCall![0].items[0].options).toEqual({ nested: [1, true] })
    expect(validate()).toBe(true)
  })
})
