import { createRef, useState } from "react"
import { describe, it, expect } from "vitest"
import { act, fireEvent, render, screen } from "@testing-library/react"
import { SchemaForm, type SchemaFormHandle } from "./SchemaForm.tsx"

function Harness({ schema, initial }: { schema: unknown; initial: unknown }) {
  const [spec, setSpec] = useState(initial)
  return <>
    <SchemaForm openAPISchema={JSON.stringify(schema)} formData={spec} onChange={setSpec} defaulting="server" />
    <output data-testid="spec">{JSON.stringify(spec)}</output>
  </>
}
const value = () => JSON.parse(screen.getByTestId("spec").textContent ?? "")

describe("server-prepared form values", () => {
  it("renders server values without filling schema defaults on mount or edits", () => {
    render(<Harness schema={{ type: "object", properties: {
      default: { type: "string", default: "client" },
      nested: { type: "object", default: {}, properties: { child: { type: "string", default: "client-child" } } },
      rows: { type: "array", minItems: 1, items: { type: "string", default: "client-row" } },
      flag: { type: "boolean", default: true },
      count: { type: "integer", default: 3 },
      text: { type: "string", default: "client" },
      nullable: { type: "string", nullable: true, default: "client" },
    } }} initial={{ default: "server", nested: {}, rows: [], flag: false, count: 0, text: "", nullable: null }} />)
    expect(screen.getByLabelText("default")).toHaveValue("server")
    expect(screen.getByLabelText("child")).toHaveValue("")
    fireEvent.change(screen.getByLabelText("default"), { target: { value: "user" } })
    expect(value()).toEqual({ default: "user", nested: {}, rows: [], flag: false, count: 0, text: "", nullable: null })
  })

  it("leaves out a plain string field the user clears, wherever it sits", () => {
    render(<Harness schema={{ type: "object", properties: {
      name: { type: "string" },
      rows: { type: "array", items: { type: "object", properties: { label: { type: "string" } } } },
      entries: { type: "object", additionalProperties: { type: "object", properties: { note: { type: "string" } } } },
    } }} initial={{ name: "server", rows: [{ label: "x" }], entries: { a: { note: "y" } } }} />)
    fireEvent.change(screen.getByLabelText("name"), { target: { value: "" } })
    fireEvent.change(screen.getByLabelText("label"), { target: { value: "" } })
    fireEvent.change(screen.getByLabelText("note"), { target: { value: "" } })
    expect(value()).toEqual({ rows: [{}], entries: { a: {} } })
  })

  it("does not write a const field or an empty array the schema asks for", () => {
    render(<Harness schema={{ type: "object", properties: {
      kind: { type: "string", const: "fixed" },
      top: { type: "string" },
      rows: { type: "array", minItems: 1, items: { type: "string" } },
    } }} initial={{}} />)
    fireEvent.change(screen.getByLabelText("top"), { target: { value: "user" } })
    expect(value()).toEqual({ top: "user" })
  })

  it("writes no const field or empty array into a dictionary value either", () => {
    render(<Harness schema={{ type: "object", properties: { entries: { type: "object", additionalProperties: {
      type: "object", properties: {
        kind: { type: "string", const: "fixed" },
        list: { type: "array", minItems: 1, items: { type: "string" } },
        label: { type: "string" },
      },
    } } } }} initial={{ entries: { a: {} } }} />)
    fireEvent.change(screen.getByLabelText("label"), { target: { value: "z" } })
    expect(value()).toEqual({ entries: { a: { label: "z" } } })
  })

  it("keeps an emptied sensitive field as an empty string", () => {
    render(<Harness schema={{ type: "object", properties: { password: { type: "string" } } }} initial={{ password: "secret" }} />)
    fireEvent.change(screen.getByLabelText("password"), { target: { value: "" } })
    expect(value()).toEqual({ password: "" })
  })

  it("lets a nested object be enabled without its defaulted fields, and still requires the others", () => {
    const schema = JSON.stringify({ type: "object", properties: { autoscaling: {
      type: "object", default: {}, required: ["enabled", "min", "name"],
      properties: { enabled: { type: "boolean", default: false }, min: { type: "integer", default: 1 }, name: { type: "string" } },
    } } })
    function validate(spec: unknown) {
      const ref = createRef<SchemaFormHandle>()
      const view = render(<SchemaForm ref={ref} openAPISchema={schema} formData={spec} onChange={() => {}} defaulting="server" />)
      let valid: boolean | undefined
      act(() => { valid = ref.current?.validate() })
      view.unmount()
      return valid
    }
    expect(validate({ autoscaling: { enabled: true, name: "x" } })).toBe(true)
    expect(validate({ autoscaling: { enabled: true } })).toBe(false)
  })

  it("still requires the fields of an array row, because the server does not complete rows", () => {
    const schema = JSON.stringify({ type: "object", properties: { rows: { type: "array", items: {
      type: "object", required: ["storage", "name"],
      properties: { storage: { type: "string", default: "10Gi" }, name: { type: "string" } },
    } } } })
    function validate(spec: unknown) {
      const ref = createRef<SchemaFormHandle>()
      const view = render(<SchemaForm ref={ref} openAPISchema={schema} formData={spec} onChange={() => {}} defaulting="server" />)
      let valid: boolean | undefined
      act(() => { valid = ref.current?.validate() })
      view.unmount()
      return valid
    }
    expect(validate({ rows: [{ name: "x", storage: "1Gi" }] })).toBe(true)
    expect(validate({ rows: [{ name: "x" }] })).toBe(false)
  })

  it("adds an array row without calculating its developer defaults", () => {
    render(<Harness schema={{ type: "object", properties: { rows: { type: "array", items: {
      type: "object", properties: { setting: { type: "string", default: "client" } },
    } } } }} initial={{ rows: [] }} />)
    fireEvent.click(screen.getByRole("button", { name: /add/i }))
    expect(screen.getByLabelText("setting")).toHaveValue("")
    expect(value()).toEqual({ rows: [{}] })
  })

  it("adds a dictionary entry without calculating its developer defaults", () => {
    render(<Harness schema={{ type: "object", properties: { entries: { type: "object", additionalProperties: {
      type: "object", properties: { setting: { type: "string", default: "client" } },
    } } } }} initial={{ entries: {} }} />)
    fireEvent.change(screen.getByPlaceholderText("Enter key name..."), { target: { value: "new" } })
    fireEvent.click(screen.getByRole("button", { name: /add/i }))
    expect(screen.getByLabelText("setting")).toHaveValue("")
    expect(value()).toEqual({ entries: { new: {} } })
  })
})
