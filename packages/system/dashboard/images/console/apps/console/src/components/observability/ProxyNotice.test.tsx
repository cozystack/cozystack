import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { ProxyError } from "../../lib/observability/proxy.ts"
import { ProxyNotice } from "./ProxyNotice.tsx"

describe("ProxyNotice", () => {
  it("renders a 404 as not available on this cluster, not as an error", () => {
    render(<ProxyNotice what="Metrics" error={new ProxyError("not-available", 404, "page not found")} />)

    expect(screen.getByRole("status")).toHaveTextContent("Metrics are not available on this cluster.")
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("renders a 502 as a quiet notice about the store", () => {
    render(<ProxyNotice what="Logs" error={new ProxyError("store-unreachable", 502, "dial tcp")} />)

    expect(screen.getByRole("status")).toHaveTextContent("The logs store is unreachable right now.")
  })

  it.each([
    ["forbidden", 403, "You do not have permission to read metrics here."],
    ["limit", 422, "too many points. Choose a shorter range."],
    ["rate-limited", 429, "Too many requests. Try again in a moment."],
    ["timeout", 504, "The store did not answer in time. Choose a shorter range or a narrower filter."],
    ["unavailable", 503, "The service is temporarily unavailable. Try again in a moment."],
  ] as const)("explains %s", (kind, status, text) => {
    render(<ProxyNotice what="Metrics" error={new ProxyError(kind, status, "too many points")} />)

    expect(screen.getByRole("alert")).toHaveTextContent(text)
  })

  it("shows the server message for a rejected query", () => {
    render(<ProxyNotice what="Logs" error={new ProxyError("bad-request", 400, "cannot parse query")} />)

    expect(screen.getByRole("alert")).toHaveTextContent("cannot parse query")
  })

  it("falls back to the message of an unexpected error", () => {
    render(<ProxyNotice what="Logs" error={new Error("boom")} />)

    expect(screen.getByRole("alert")).toHaveTextContent("boom")
  })
})
