import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/react"
import type { ObjectFieldTemplateProps, ObjectFieldTemplatePropertyType } from "@rjsf/utils"
import { CustomObjectFieldTemplate } from "./CustomObjectFieldTemplate.tsx"

const prop = (name: string): ObjectFieldTemplatePropertyType => ({
  name,
  content: <div data-testid={`field-${name}`} />,
  disabled: false,
  readonly: false,
  hidden: false,
})

// One cast at the fixture boundary keeps every render call below fully typed;
// `registry` is a large RJSF object this template never reads.
const base = {
  idSchema: { $id: "root_addons_cilium" },
  schema: { "x-cozystack-no-enable-switch": true },
  title: "cilium",
  description: "Cilium CNI plugin.",
  formData: {},
  properties: [],
  onAddClick: () => () => {},
  registry: {},
} as unknown as ObjectFieldTemplateProps

describe("addons with no enable switch", () => {
  it("says the switch is absent when the schema declares it", () => {
    render(<CustomObjectFieldTemplate {...base} properties={[prop("valuesOverride")]} />)
    expect(screen.getByText(/No enable switch/)).toBeTruthy()
    expect(screen.queryByRole("checkbox")).toBeNull()
  })

  it("warns that an enabled field in YAML does nothing", () => {
    render(<CustomObjectFieldTemplate {...base} properties={[prop("valuesOverride")]} />)
    expect(screen.getByText(/adding an enabled field in YAML has no effect/i)).toBeTruthy()
  })

  // The three addons in this class are not all always-on: verticalPodAutoscaler
  // follows addons.monitoringAgents.enabled. Only the schema description knows
  // which is which, so the fixed copy must not claim any of them is mandatory.
  it("leaves the reason to the schema description", () => {
    render(
      <CustomObjectFieldTemplate
        {...base}
        title="verticalPodAutoscaler"
        description="Vertical Pod Autoscaler. Installed and removed together with addons.monitoringAgents.enabled; this section only overrides its Helm values."
        properties={[prop("valuesOverride")]}
      />,
    )
    // Anchor on the no-toggle branch: the default renderer also prints the
    // description and neither forbidden phrase, so without this the assertions
    // below hold whether or not the branch was taken.
    expect(screen.getByText(/No enable switch/)).toBeTruthy()
    expect(screen.getByText(/together with addons.monitoringAgents.enabled/)).toBeTruthy()
    expect(screen.queryByText(/cannot be disabled/)).toBeNull()
    expect(screen.queryByText(/Always on/)).toBeNull()
  })

  it("does not infer addon copy from a valuesOverride-only shape", () => {
    render(
      <CustomObjectFieldTemplate
        {...base}
        schema={{}}
        properties={[prop("valuesOverride")]}
      />,
    )
    expect(screen.queryByText(/No enable switch/)).toBeNull()
    expect(screen.getByTestId("field-valuesOverride")).toBeTruthy()
  })

  it("keeps the notice when another configuration field is added", () => {
    render(
      <CustomObjectFieldTemplate
        {...base}
        properties={[prop("valuesOverride"), prop("config")]}
      />,
    )
    expect(screen.getByText(/No enable switch/)).toBeTruthy()
    expect(screen.getByTestId("field-config")).toBeTruthy()
  })

  it("leaves a toggleable addon alone", () => {
    render(
      <CustomObjectFieldTemplate
        {...base}
        properties={[prop("enabled"), prop("valuesOverride")]}
      />,
    )
    expect(screen.queryByText(/No enable switch/)).toBeNull()
    expect(screen.getByTestId("field-enabled")).toBeTruthy()
  })
})
