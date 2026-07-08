import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { MemoryRouter } from "react-router"
import { ClusterUsageAggregates } from "./ClusterUsageAggregates.tsx"
import type { AggregateResources } from "../../lib/cluster-usage/types.ts"
import type { NodeSummary } from "../../hooks/useClusterUsageData.tsx"

function empty(): AggregateResources {
  return {
    standard: {
      cpu: { capacity: 0, allocatable: 0, requested: 0 },
      memory: { capacity: 0, allocatable: 0, requested: 0 },
      "ephemeral-storage": { capacity: 0, allocatable: 0, requested: 0 },
      pods: { capacity: 0, allocatable: 0, requested: 0 },
    },
    extended: {},
  }
}

function summary(overrides: Partial<NodeSummary> = {}): NodeSummary {
  return { total: 0, ready: 0, notReady: 0, schedulingDisabled: 0, ...overrides }
}

function renderAgg(props: Parameters<typeof ClusterUsageAggregates>[0]) {
  return render(
    <MemoryRouter>
      <ClusterUsageAggregates {...props} />
    </MemoryRouter>,
  )
}

function rowLabels(container: HTMLElement): (string | null)[] {
  return Array.from(container.querySelectorAll("[data-resource-row]")).map((el) =>
    el.getAttribute("data-resource-row"),
  )
}

describe("ClusterUsageAggregates", () => {
  it("renders the node-summary header line", () => {
    renderAgg({
      aggregates: empty(),
      nodeSummary: summary({ total: 12, ready: 10, notReady: 1, schedulingDisabled: 1 }),
    })
    expect(screen.getByText("12 nodes")).toBeInTheDocument()
    expect(
      screen.getByText(/10 Ready · 1 NotReady · 1 SchedulingDisabled/),
    ).toBeInTheDocument()
  })

  it("uses singular 'node' in the header for a one-node cluster", () => {
    renderAgg({ aggregates: empty(), nodeSummary: summary({ total: 1, ready: 1 }) })
    expect(screen.getByText("1 node")).toBeInTheDocument()
  })

  it("renders the standard resources as rows top-to-bottom in order CPU, Memory, Storage, Pods", () => {
    const { container } = renderAgg({ aggregates: empty(), nodeSummary: summary() })
    expect(rowLabels(container)).toEqual(["CPU", "Memory", "Storage", "Pods"])
  })

  it("appends extended-resource rows after the standard rows, sorted alphabetically by key", () => {
    const agg = empty()
    agg.extended["nvidia.com/gpu"] = { capacity: 4, allocatable: 4, requested: 1 }
    agg.extended["amd.com/gpu"] = { capacity: 2, allocatable: 2, requested: 0 }
    const { container } = renderAgg({ aggregates: agg, nodeSummary: summary() })
    expect(rowLabels(container)).toEqual([
      "CPU",
      "Memory",
      "Storage",
      "Pods",
      "amd.com/gpu",
      "nvidia.com/gpu",
    ])
  })

  it("links requestable resource rows to the per-resource drill-down", () => {
    const agg = empty()
    agg.extended["nvidia.com/gpu"] = { capacity: 4, allocatable: 4, requested: 1 }
    renderAgg({ aggregates: agg, nodeSummary: summary() })
    expect(screen.getByRole("link", { name: "CPU" })).toHaveAttribute(
      "href",
      "/admin/capacity/cluster/r/cpu",
    )
    expect(screen.getByRole("link", { name: "nvidia.com/gpu" })).toHaveAttribute(
      "href",
      "/admin/capacity/cluster/r/nvidia.com/gpu",
    )
  })

  it("does not link the Pods count row (not a requestable resource)", () => {
    renderAgg({ aggregates: empty(), nodeSummary: summary() })
    expect(screen.queryByRole("link", { name: "Pods" })).toBeNull()
    expect(screen.getByText("Pods")).toBeInTheDocument()
  })

  it("shows an em-dash in the Used cell when no usage data is present", () => {
    const agg = empty()
    agg.standard.cpu = { capacity: 8, allocatable: 8, requested: 2 }
    const { container } = renderAgg({ aggregates: agg, nodeSummary: summary({ total: 1 }) })
    const cpuRow = container.querySelector('[data-resource-row="CPU"]') as HTMLElement
    const cells = cpuRow.querySelectorAll("td")
    // Last column is Used.
    expect(cells[cells.length - 1].textContent).toBe("—")
  })

  it("shows the Used value when usage data is present", () => {
    const agg = empty()
    agg.standard.cpu = { capacity: 8, allocatable: 8, requested: 2, used: 1 }
    const { container } = renderAgg({ aggregates: agg, nodeSummary: summary({ total: 1 }) })
    const cpuRow = container.querySelector('[data-resource-row="CPU"]') as HTMLElement
    const cells = cpuRow.querySelectorAll("td")
    expect(cells[cells.length - 1].textContent).not.toBe("—")
  })

  it("replaces Requested numbers with an em-dash tooltip when pods are unavailable", () => {
    const agg = empty()
    agg.standard.cpu = { capacity: 8, allocatable: 8, requested: 3 }
    renderAgg({
      aggregates: agg,
      nodeSummary: summary({ total: 1, ready: 1 }),
      podsUnavailable: true,
    })
    expect(
      screen.getAllByTitle("Requires cluster-wide pod read access").length,
    ).toBeGreaterThan(0)
  })
})
