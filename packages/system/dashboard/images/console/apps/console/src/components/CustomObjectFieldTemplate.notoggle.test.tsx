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
  schema: {},
  title: "cilium",
  description: "Cilium CNI plugin.",
  formData: {},
  properties: [],
  onAddClick: () => () => {},
  registry: {},
} as unknown as ObjectFieldTemplateProps

describe("addons with no enable switch", () => {
  it("says the switch is absent when the object carries only valuesOverride", () => {
    render(<CustomObjectFieldTemplate {...base} properties={[prop("valuesOverride")]} />)
    expect(screen.getByText(/No enable switch/)).toBeTruthy()
    expect(screen.queryByRole("checkbox")).toBeNull()
  })

  it("warns that an enabled field in YAML does nothing", () => {
    render(<CustomObjectFieldTemplate {...base} properties={[prop("valuesOverride")]} />)
    expect(screen.getByText(/adding an enabled field in YAML has no effect/)).toBeTruthy()
  })

  // The three addons in this class are not all always-on: verticalPodAutoscaler
  // follows addons.monitoringAgents.enabled. Only the schema description knows
  // which is which, so the fixed copy must not claim any of them is mandatory.
  it("leaves the reason to the schema description", () => {
    render(
      <CustomObjectFieldTemplate
        {...base}
        title="verticalPodAutoscaler"
        description="Vertical Pod Autoscaler. Has no enable switch of its own: it is installed and removed together with addons.monitoringAgents.enabled, so this section only overrides its Helm values."
        properties={[prop("valuesOverride")]}
      />,
    )
    expect(screen.getByText(/together with addons.monitoringAgents.enabled/)).toBeTruthy()
    expect(screen.queryByText(/cannot be disabled/)).toBeNull()
    expect(screen.queryByText(/Always on/)).toBeNull()
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
