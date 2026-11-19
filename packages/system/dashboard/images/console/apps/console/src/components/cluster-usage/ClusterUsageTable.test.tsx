import { describe, it, expect } from "vitest"
import { render as rtlRender, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { MemoryRouter } from "react-router"
import { ClusterUsageTable } from "./ClusterUsageTable.tsx"
import type { NodeRow } from "../../lib/cluster-usage/types.ts"

// The table renders <Link>s for resource row labels, so every render needs a
// router context.
function render(ui: Parameters<typeof rtlRender>[0]) {
  return rtlRender(<MemoryRouter>{ui}</MemoryRouter>)
}

function row(name: string, overrides: Partial<NodeRow> = {}): NodeRow {
  return {
    name,
    ready: true,
    schedulable: true,
    pressureConditions: [],
    roles: [],
    taints: [],
    age: "1d",
    creationTimestamp: "2026-05-25T00:00:00Z",
    standard: {
      cpu: { capacity: 8, allocatable: 8, requested: 1 },
      memory: { capacity: 16 * 1024 ** 3, allocatable: 16 * 1024 ** 3, requested: 0 },
      "ephemeral-storage": { capacity: 0, allocatable: 0, requested: 0 },
      pods: { capacity: 110, allocatable: 110, requested: 0 },
    },
    extended: {},
    ...overrides,
  }
}

function attrRow(container: HTMLElement, key: string): HTMLElement {
  return container.querySelector(`[data-attribute-row="${key}"]`) as HTMLElement
}

function nodeCols(container: HTMLElement): (string | null)[] {
  return Array.from(container.querySelectorAll("[data-node-col]")).map((el) =>
    el.getAttribute("data-node-col"),
  )
}

function thead(container: HTMLElement): HTMLElement {
  return container.querySelector("thead") as HTMLElement
}

describe("ClusterUsageTable (transposed: nodes are columns)", () => {
  it("renders one column per node, sorted by name ascending", () => {
    const { container } = render(
      <ClusterUsageTable rows={[row("worker-b"), row("worker-a")]} extendedKeys={[]} />,
    )
    expect(nodeCols(container)).toEqual(["worker-a", "worker-b"])
  })

  it("lays out resource rows top-to-bottom: CPU, Memory, then extended in order", () => {
    const { container } = render(
      <ClusterUsageTable
        rows={[row("n", { extended: { "nvidia.com/gpu": { capacity: 2, allocatable: 2, requested: 1 } } })]}
        extendedKeys={["nvidia.com/gpu", "amd.com/gpu"]}
      />,
    )
    const order = Array.from(container.querySelectorAll("[data-attribute-row]")).map((el) =>
      el.getAttribute("data-attribute-row"),
    )
    expect(order).toEqual(["cpu", "memory", "nvidia.com/gpu", "amd.com/gpu"])
  })

  it("links resource row labels (CPU, Memory, extended) to the per-resource drill-down", () => {
    render(
      <ClusterUsageTable
        rows={[row("n", { extended: { "nvidia.com/gpu": { capacity: 2, allocatable: 2, requested: 1 } } })]}
        extendedKeys={["nvidia.com/gpu"]}
      />,
    )
    expect(screen.getByRole("link", { name: "CPU" })).toHaveAttribute(
      "href",
      "/admin/capacity/cluster/r/cpu",
    )
    expect(screen.getByRole("link", { name: "Memory" })).toHaveAttribute(
      "href",
      "/admin/capacity/cluster/r/memory",
    )
    expect(screen.getByRole("link", { name: "nvidia.com/gpu" })).toHaveAttribute(
      "href",
      "/admin/capacity/cluster/r/nvidia.com/gpu",
    )
    // The node name in the header is not a link.
    expect(screen.queryByRole("link", { name: "n" })).toBeNull()
  })

  it("renders Status / Roles / Age inside each node's column header, not as rows", () => {
    const { container } = render(
      <ClusterUsageTable
        rows={[
          row("ok", { ready: true, roles: ["control-plane"], age: "21h" }),
          row("bad", { ready: false, roles: [] }),
          row("cordoned", { schedulable: false }),
        ]}
        extendedKeys={[]}
      />,
    )
    const head = thead(container)
    expect(within(head).getByText("Ready")).toBeInTheDocument()
    expect(within(head).getByText("NotReady")).toBeInTheDocument()
    expect(within(head).getByText(/scheduling.?disabled/i)).toBeInTheDocument()
    expect(within(head).getByText("control-plane")).toBeInTheDocument()
    expect(within(head).getByText(/21h/)).toBeInTheDocument()
    // No Status/Roles/Age body rows remain.
    expect(attrRow(container, "status")).toBeNull()
    expect(attrRow(container, "age")).toBeNull()
  })

  it("flags pressure conditions with a chip in the header", () => {
    const { container } = render(
      <ClusterUsageTable
        rows={[row("pressured", { pressureConditions: ["MemoryPressure"] })]}
        extendedKeys={[]}
      />,
    )
    expect(within(thead(container)).getByText("MemoryPressure")).toBeInTheDocument()
  })

  it("renders an em dash for a node header without roles", () => {
    const { container } = render(
      <ClusterUsageTable rows={[row("worker", { roles: [] })]} extendedKeys={[]} />,
    )
    expect(within(thead(container)).getByText("—")).toBeInTheDocument()
  })

  it("renders em dash in an extended-resource row for a node that does not expose it", () => {
    const { container } = render(
      <ClusterUsageTable rows={[row("plain", { extended: {} })]} extendedKeys={["nvidia.com/gpu"]} />,
    )
    expect(within(attrRow(container, "nvidia.com/gpu")).getByText("—")).toBeInTheDocument()
  })

  it("collapses extended-resource cells to em dash for a NotReady node", () => {
    const gpu = { "nvidia.com/gpu": { capacity: 2, allocatable: 2, requested: 1 } }
    const { container } = render(
      <ClusterUsageTable
        rows={[row("ready-gpu", { ready: true, extended: gpu }), row("down-gpu", { ready: false, extended: gpu })]}
        extendedKeys={["nvidia.com/gpu"]}
      />,
    )
    expect(within(attrRow(container, "nvidia.com/gpu")).getAllByText("capacity 2")).toHaveLength(1)
  })

  it("renders em dashes in the CPU and Memory rows when the node is NotReady", () => {
    const { container } = render(
      <ClusterUsageTable rows={[row("dead", { ready: false })]} extendedKeys={[]} />,
    )
    expect(within(attrRow(container, "cpu")).getByText("—")).toBeInTheDocument()
    expect(within(attrRow(container, "memory")).getByText("—")).toBeInTheDocument()
  })

  it("hides a node column when filtered out by name (case-insensitive)", async () => {
    const user = userEvent.setup()
    const { container } = render(
      <ClusterUsageTable
        rows={[row("worker-cpu-1"), row("worker-gpu-1"), row("ctrl-1")]}
        extendedKeys={[]}
      />,
    )
    await user.type(screen.getByLabelText("Filter nodes"), "GPU")
    expect(nodeCols(container)).toEqual(["worker-gpu-1"])
  })

  it("filters node columns by role substring", async () => {
    const user = userEvent.setup()
    const { container } = render(
      <ClusterUsageTable
        rows={[row("a", { roles: ["control-plane"] }), row("b", { roles: ["worker"] })]}
        extendedKeys={[]}
      />,
    )
    await user.type(screen.getByLabelText("Filter nodes"), "control")
    expect(nodeCols(container)).toEqual(["a"])
  })

  it("replaces the Requested line with an em-dash tooltip when podsUnavailable", () => {
    const { container } = render(
      <ClusterUsageTable
        rows={[
          row("loaded", {
            standard: {
              cpu: { capacity: 8, allocatable: 8, requested: 4 },
              memory: { capacity: 16 * 1024 ** 3, allocatable: 16 * 1024 ** 3, requested: 0 },
              "ephemeral-storage": { capacity: 0, allocatable: 0, requested: 0 },
              pods: { capacity: 110, allocatable: 110, requested: 0 },
            },
          }),
        ]}
        extendedKeys={[]}
        podsUnavailable
      />,
    )
    const cpu = attrRow(container, "cpu")
    expect(
      cpu.querySelectorAll('[title="Requires cluster-wide pod read access"]').length,
    ).toBeGreaterThan(0)
    expect(within(cpu).queryByText(/4 \/ 8 req/)).toBeNull()
  })
})
