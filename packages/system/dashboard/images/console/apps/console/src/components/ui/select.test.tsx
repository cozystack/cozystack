import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { SelectItem, SelectLabel, SelectPopup, SelectRoot, SelectTrigger, SelectValue } from "./select.tsx"

const items = [
  { value: "", label: "All" },
  { value: "db-0", label: "db-0" },
  { value: "db-1", label: "db-1" },
]

function PodSelect({ value = "", onValueChange = () => {} }: { value?: string; onValueChange?: (v: string) => void }) {
  return (
    <SelectRoot items={items} value={value} onValueChange={(v) => onValueChange(v ?? "")}>
      <SelectLabel>Pod</SelectLabel>
      <SelectTrigger>
        <SelectValue />
      </SelectTrigger>
      <SelectPopup>
        {items.map((i) => (
          <SelectItem key={i.value} value={i.value}>
            {i.label}
          </SelectItem>
        ))}
      </SelectPopup>
    </SelectRoot>
  )
}

describe("Select", () => {
  it("names the trigger by its label and shows the chosen item", () => {
    render(<PodSelect value="db-1" />)

    expect(screen.getByRole("combobox", { name: "Pod" })).toHaveTextContent("db-1")
  })

  it("shows the label of an empty-string value", () => {
    render(<PodSelect />)

    expect(screen.getByRole("combobox", { name: "Pod" })).toHaveTextContent("All")
  })

  it("lists the items and reports the one clicked", async () => {
    const onValueChange = vi.fn()
    render(<PodSelect onValueChange={onValueChange} />)

    await userEvent.click(screen.getByRole("combobox", { name: "Pod" }))
    await userEvent.click(await screen.findByRole("option", { name: "db-1" }))

    expect(onValueChange).toHaveBeenCalledWith("db-1")
  })

  it("can be driven from the keyboard", async () => {
    const onValueChange = vi.fn()
    render(<PodSelect onValueChange={onValueChange} />)

    screen.getByRole("combobox", { name: "Pod" }).focus()
    await userEvent.keyboard("{ArrowDown}")
    expect(await screen.findByRole("listbox")).toBeInTheDocument()
    await userEvent.keyboard("{ArrowDown}{Enter}")

    expect(onValueChange).toHaveBeenCalledWith("db-0")
  })
})
