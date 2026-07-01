import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"
import { K8sClient, K8sProvider, useK8sClient } from "@cozystack/k8s-client"

function ClientCapture({ onClient }: { onClient: (c: K8sClient) => void }) {
  const c = useK8sClient()
  onClient(c)
  return null
}

describe("K8sProvider", () => {
  it("passes the injected client through to useK8sClient", () => {
    const injected = new K8sClient({ baseUrl: "/injected" })
    let captured: K8sClient | null = null
    render(
      <K8sProvider client={injected}>
        <ClientCapture onClient={(c) => (captured = c)} />
      </K8sProvider>,
    )
    expect(captured).toBe(injected)
  })

  it("constructs its own client when none is injected", () => {
    let captured: K8sClient | null = null
    render(
      <K8sProvider>
        <ClientCapture onClient={(c) => (captured = c)} />
      </K8sProvider>,
    )
    expect(captured).toBeInstanceOf(K8sClient)
  })

  it("constructs a client from the provided config when no client is injected", () => {
    let captured: K8sClient | null = null
    render(
      <K8sProvider config={{ baseUrl: "/from-config" }}>
        <ClientCapture onClient={(c) => (captured = c)} />
      </K8sProvider>,
    )
    expect(captured).toBeInstanceOf(K8sClient)
  })

  it("prefers the injected client over the config when both are supplied", () => {
    const injected = new K8sClient({ baseUrl: "/injected" })
    let captured: K8sClient | null = null
    render(
      <K8sProvider client={injected} config={{ baseUrl: "/ignored" }}>
        <ClientCapture onClient={(c) => (captured = c)} />
      </K8sProvider>,
    )
    expect(captured).toBe(injected)
  })
})
