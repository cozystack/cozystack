import { describe, expect, it } from "vitest"
import { resolveSource } from "./DynamicOptionsWidget.tsx"

describe("resolveSource", () => {
  it("leaves a plain source alone", () => {
    expect(resolveSource("storageclass", {})).toEqual({
      name: "storageclass",
      parameterised: false,
    })
  })

  it("substitutes a sibling field", () => {
    const root = { sourceRef: { name: "vcenter-lab" } }
    expect(resolveSource("vmimportvm.{sourceRef.name}", root)).toEqual({
      name: "vmimportvm.vcenter-lab",
      parameterised: true,
    })
  })

  // The field it depends on is still empty. Asking for `vmimportvm.` would only
  // 404, and the widget shows "select a source first" instead.
  it("yields no name while the referenced field is unset", () => {
    expect(resolveSource("vmimportvm.{sourceRef.name}", {})).toEqual({
      name: null,
      parameterised: true,
    })
    expect(resolveSource("vmimportvm.{sourceRef.name}", { sourceRef: {} })).toEqual({
      name: null,
      parameterised: true,
    })
    expect(
      resolveSource("vmimportvm.{sourceRef.name}", { sourceRef: { name: "" } }),
    ).toEqual({ name: null, parameterised: true })
  })

  // A path through something that is not an object must not throw: form data is
  // whatever the user has typed so far.
  it("survives a path that does not lead anywhere", () => {
    expect(resolveSource("vmimportvm.{a.b.c}", { a: "string" })).toEqual({
      name: null,
      parameterised: true,
    })
    expect(resolveSource("vmimportvm.{a.b}", null)).toEqual({
      name: null,
      parameterised: true,
    })
  })

  it("treats a missing source as nothing to resolve", () => {
    expect(resolveSource(undefined, {})).toEqual({ name: null, parameterised: false })
  })
})
