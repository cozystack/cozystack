import { describe, it, expect, beforeEach } from "vitest"
import { focusFirstError } from "@/lib/focus-first-error.ts"

describe("focusFirstError", () => {
  beforeEach(() => {
    document.body.innerHTML = ""
  })

  it("focuses the field carrying the generated id", () => {
    document.body.innerHTML = `<input id="root_name" />`
    const field = document.getElementById("root_name")

    focusFirstError({ property: ".name" })

    expect(document.activeElement).toBe(field)
  })

  it("resolves a bracketed property path", () => {
    document.body.innerHTML = `<input id="root_spec_first-name" />`
    const field = document.getElementById("root_spec_first-name")

    focusFirstError({ property: ".spec['first-name']" })

    expect(document.activeElement).toBe(field)
  })

  it("preserves a dot inside a bracketed property name", () => {
    document.body.innerHTML = `<input id="root_spec_foo.bar" />`
    const field = document.getElementById("root_spec_foo.bar")

    focusFirstError({ property: ".spec['foo.bar']" })

    expect(document.activeElement).toBe(field)
  })

  it("uses the same custom prefix and separator as the form", () => {
    document.body.innerHTML = `<input id="form/spec/name" />`
    const field = document.getElementById("form/spec/name")

    focusFirstError({ property: ".spec.name" }, "form", "/")

    expect(document.activeElement).toBe(field)
  })

  // SourceField and SourceWidget render no element carrying the generated id,
  // only radios named `<id>-source`, so the exact lookup misses and a blocked
  // submit used to scroll nowhere.
  it("falls back to the name attribute when no element carries the id", () => {
    document.body.innerHTML = `
      <div id="root_source-help" class="field">
        <input type="radio" name="root_source-source" value="http" />
        <input type="radio" name="root_source-source" value="pvc" />
      </div>
    `
    const first = document.querySelector<HTMLInputElement>('input[value="http"]')

    focusFirstError({ property: ".source" })

    expect(document.getElementById("root_source")).toBeNull()
    expect(document.activeElement).toBe(first)
  })

  // A bare prefix match would grab any sibling whose id merely starts with the
  // same string, scrolling to a field the user was not asked about.
  it("does not fall back to a sibling that merely shares the prefix", () => {
    document.body.innerHTML = `<input id="root_database" />`

    focusFirstError({ property: ".data" })

    expect(document.getElementById("root_data")).toBeNull()
    expect(document.activeElement).toBe(document.body)
  })

  it("falls back to a nested input under the errored object", () => {
    document.body.innerHTML = `
      <input id="root_database" />
      <input id="root_data_host" />
    `
    const nested = document.getElementById("root_data_host")

    focusFirstError({ property: ".data" })

    expect(document.activeElement).toBe(nested)
  })

  it("does nothing when the field is nowhere in the document", () => {
    document.body.innerHTML = `<input id="root_other" />`

    expect(() => focusFirstError({ property: ".missing" })).not.toThrow()
    expect(document.activeElement).toBe(document.body)
  })
})
