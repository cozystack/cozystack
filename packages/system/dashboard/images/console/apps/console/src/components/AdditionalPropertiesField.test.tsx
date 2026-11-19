import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { SchemaForm } from "./SchemaForm.tsx"
import { IMMUTABLE_HELP_TEXT } from "../lib/immutable-paths.ts"

const schemaWithMap = JSON.stringify({
  type: "object",
  properties: {
    labels: {
      type: "object",
      additionalProperties: { type: "string" },
      "x-kubernetes-validations": [
        { rule: "self == oldSelf", message: "labels are immutable" },
      ],
    },
  },
})

const noop = () => {}

describe("AdditionalPropertiesField immutable", () => {
  it("renders Add/Remove controls when not immutable", () => {
    render(
      <SchemaForm
        openAPISchema={schemaWithMap}
        formData={{ labels: { env: "prod" } }}
        onChange={noop}
      />,
    )
    expect(screen.getByPlaceholderText("Enter key name...")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /add/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /remove/i })).toBeInTheDocument()
  })

  it("treats nested *-not-last on additionalProperties as whole-map disabled in the UI; overlay matches", () => {
    // Per-value-immutable schema (additionalProperties carries a nested
    // CEL rule). The UI freezes the whole map and the overlay freezes the
    // whole map (see overlayImmutable's additionalProperties branch).
    // Both sides therefore agree: YAML-editor bypasses that add or rename
    // keys are caught by the overlay before the PUT lands.
    const nestedImmutableSchema = JSON.stringify({
      type: "object",
      properties: {
        labels: {
          type: "object",
          additionalProperties: {
            type: "object",
            properties: {
              value: {
                type: "string",
                "x-kubernetes-validations": [
                  { rule: "self == oldSelf", message: "value is immutable" },
                ],
              },
            },
          },
        },
      },
    })
    render(
      <SchemaForm
        openAPISchema={nestedImmutableSchema}
        formData={{ labels: { env: { value: "prod" } } }}
        onChange={noop}
        immutableMode="enforce"
      />,
    )
    expect(
      screen.queryByPlaceholderText("Enter key name..."),
    ).not.toBeInTheDocument()
    expect(screen.getByText(IMMUTABLE_HELP_TEXT)).toBeInTheDocument()
  })

  it("hides Add/Remove and disables inner fields when the map is immutable in enforce mode", () => {
    render(
      <SchemaForm
        openAPISchema={schemaWithMap}
        formData={{ labels: { env: "prod" } }}
        onChange={noop}
        immutableMode="enforce"
      />,
    )
    expect(
      screen.queryByPlaceholderText("Enter key name..."),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: /add/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: /remove/i }),
    ).not.toBeInTheDocument()

    const innerInput = screen.getByDisplayValue("prod") as HTMLInputElement
    expect(innerInput).toBeDisabled()

    expect(screen.getByText(IMMUTABLE_HELP_TEXT)).toBeInTheDocument()
  })
})
