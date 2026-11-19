import { useState } from "react"
import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { WidgetProps } from "@rjsf/utils"
import { SensitiveStringWidget } from "./SensitiveStringWidget.tsx"

function makeProps(overrides: Partial<WidgetProps> = {}): WidgetProps {
  const base = {
    id: "sensitive",
    name: "sensitive",
    label: "sensitive",
    value: undefined as unknown,
    onChange: vi.fn(),
    onBlur: vi.fn(),
    onFocus: vi.fn(),
    required: false,
    disabled: false,
    readonly: false,
    autofocus: false,
    placeholder: "",
    options: {},
    schema: { type: "string" },
    uiSchema: {},
    formContext: {},
    rawErrors: [],
    hideError: false,
    multiple: false,
    registry: {},
  }
  return { ...base, ...overrides } as unknown as WidgetProps
}

function renderWithLabel(props: WidgetProps) {
  return render(
    <>
      <label htmlFor={props.id}>access key</label>
      <SensitiveStringWidget {...props} />
    </>,
  )
}

const TOGGLE_NAME = /toggle credential visibility/i

describe("SensitiveStringWidget", () => {
  it("renders a real <input> control associated with its label, regardless of state", () => {
    renderWithLabel(makeProps({ value: "super-secret" }))

    const input = screen.getByLabelText("access key") as HTMLInputElement
    expect(input.tagName).toBe("INPUT")
    expect(input.type).toBe("password")
    expect(input.value).toBe("super-secret")
  })

  it("flips the input type from password to text when the toggle is pressed", async () => {
    const user = userEvent.setup()
    renderWithLabel(makeProps({ value: "super-secret" }))

    const toggle = screen.getByRole("button", { name: TOGGLE_NAME })
    expect(toggle).toHaveAttribute("aria-pressed", "false")

    await user.click(toggle)

    expect(toggle).toHaveAttribute("aria-pressed", "true")
    expect((screen.getByLabelText("access key") as HTMLInputElement).type).toBe("text")
  })

  it("keeps the toggle's accessible name stable across state changes", async () => {
    const user = userEvent.setup()
    renderWithLabel(makeProps({ value: "super-secret" }))

    const toggle = screen.getByRole("button", { name: TOGGLE_NAME })
    await user.click(toggle)

    // Same button is still findable by the same stable name after pressing.
    expect(screen.getByRole("button", { name: TOGGLE_NAME })).toBe(toggle)
  })

  it("hides the value again after a second toggle press", async () => {
    const user = userEvent.setup()
    renderWithLabel(makeProps({ value: "super-secret" }))

    const toggle = screen.getByRole("button", { name: TOGGLE_NAME })
    await user.click(toggle)
    await user.click(toggle)

    expect(toggle).toHaveAttribute("aria-pressed", "false")
    expect((screen.getByLabelText("access key") as HTMLInputElement).type).toBe("password")
  })

  it("carries the project's input styling in both states (no shape change on toggle)", async () => {
    const user = userEvent.setup()
    renderWithLabel(makeProps({ value: "super-secret" }))

    const input = screen.getByLabelText("access key") as HTMLInputElement
    expect(input).toHaveClass("rounded-md")
    expect(input).toHaveClass("border")

    await user.click(screen.getByRole("button", { name: TOGGLE_NAME }))

    expect(input).toHaveClass("rounded-md")
    expect(input).toHaveClass("border")
  })

  it("opts the input out of browser password-manager autofill", () => {
    renderWithLabel(makeProps({ value: "super-secret" }))

    const input = screen.getByLabelText("access key") as HTMLInputElement
    expect(input).toHaveAttribute("autocomplete", "new-password")
  })

  it("forwards typing into onChange without requiring the toggle first", async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()

    function Host() {
      const [value, setValue] = useState<string | undefined>("")
      return (
        <>
          <label htmlFor="sensitive">access key</label>
          <SensitiveStringWidget
            {...makeProps({
              value,
              onChange: (next) => {
                onChange(next)
                setValue(next as string | undefined)
              },
            })}
          />
        </>
      )
    }
    render(<Host />)

    await user.type(screen.getByLabelText("access key"), "abc")

    expect(onChange).toHaveBeenLastCalledWith("abc")
  })

  it("emits undefined when the input is cleared", async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    renderWithLabel(makeProps({ value: "secret", onChange }))

    await user.clear(screen.getByLabelText("access key") as HTMLInputElement)

    expect(onChange).toHaveBeenLastCalledWith(undefined)
  })

  it("keeps the reveal toggle usable when the form is readonly", async () => {
    const user = userEvent.setup()
    renderWithLabel(makeProps({ value: "secret", readonly: true }))

    const input = screen.getByLabelText("access key") as HTMLInputElement
    expect(input).toHaveAttribute("readonly")

    await user.click(screen.getByRole("button", { name: TOGGLE_NAME }))

    expect(input.type).toBe("text")
    expect(input.value).toBe("secret")
  })

  it("keeps the reveal toggle usable when the form is disabled", async () => {
    const user = userEvent.setup()
    renderWithLabel(makeProps({ value: "secret", disabled: true }))

    const input = screen.getByLabelText("access key") as HTMLInputElement
    expect(input).toBeDisabled()

    await user.click(screen.getByRole("button", { name: TOGGLE_NAME }))

    expect(input.type).toBe("text")
  })
})
