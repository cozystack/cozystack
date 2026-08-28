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

  // RJSF flattens the instance path with dots, so this is the shape a nested
  // field actually arrives as. A decoy sharing the prefix keeps the assertion
  // from passing on the descendant fallback alone.
  it("resolves a nested property path", () => {
    document.body.innerHTML = `
      <input id="root_spec_firstname_decoy" />
      <input id="root_spec_firstname" />
    `
    const field = document.getElementById("root_spec_firstname")

    focusFirstError({ property: ".spec.firstname" })

    expect(document.activeElement).toBe(field)
  })

  it("resolves an array item path", () => {
    document.body.innerHTML = `<input id="root_items_0_name" />`
    const field = document.getElementById("root_items_0_name")

    focusFirstError({ property: ".items.0.name" })

    expect(document.activeElement).toBe(field)
  })

  // Pinned gap: RJSF gives the same dotted path whether the dot separates two
  // properties or sits inside one name, so a map keyed `ghcr.io` cannot be told
  // apart from a nested `ghcr` -> `io` and its field is never focused. The
  // submit still blocks and the inline error still renders; only the scroll is
  // lost. Fixing it needs the schema, not the error.
  it("does not resolve a property whose own name contains a dot", () => {
    document.body.innerHTML = `<input id="root_talos_registryMirrors_ghcr.io" />`

    focusFirstError({ property: ".talos.registryMirrors.ghcr.io" })

    expect(document.activeElement).toBe(document.body)
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

  // The x-cozystack-options fields (storageClass, backupClass, VM disks, GPU
  // names) render through DynamicOptionsWidget as a select, not an input.
  it("focuses a select carrying the generated id", () => {
    document.body.innerHTML = `<select id="root_gpus_0_name"></select>`
    const field = document.getElementById("root_gpus_0_name")

    focusFirstError({ property: ".gpus.0.name" })

    expect(document.activeElement).toBe(field)
  })

  it("falls back to a nested select under the errored object", () => {
    document.body.innerHTML = `<select id="root_gpus_0_name"></select>`
    const field = document.getElementById("root_gpus_0_name")

    focusFirstError({ property: ".gpus.0" })

    expect(document.activeElement).toBe(field)
  })

  it("does nothing when the field is nowhere in the document", () => {
    document.body.innerHTML = `<input id="root_other" />`

    expect(() => focusFirstError({ property: ".missing" })).not.toThrow()
    expect(document.activeElement).toBe(document.body)
  })
})
