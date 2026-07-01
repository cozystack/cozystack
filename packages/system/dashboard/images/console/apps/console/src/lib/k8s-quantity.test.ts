import { describe, it, expect } from "vitest"
import { parseQuantity, humanizeBytes, humanizeCpu } from "./k8s-quantity.ts"

describe("parseQuantity", () => {
  it("returns 0 for the empty string", () => {
    expect(parseQuantity("")).toBe(0)
  })

  it("parses milli suffix to a fractional value", () => {
    expect(parseQuantity("500m")).toBe(0.5)
  })

  it("parses milli values greater than one core", () => {
    expect(parseQuantity("1500m")).toBe(1.5)
  })

  it("parses Ki as 1024", () => {
    expect(parseQuantity("1Ki")).toBe(1024)
  })

  it("parses Mi as 1024 squared", () => {
    expect(parseQuantity("1Mi")).toBe(1024 ** 2)
  })

  it("parses Gi as 1024 cubed", () => {
    expect(parseQuantity("1Gi")).toBe(1024 ** 3)
  })

  it("parses Ti as 1024 to the fourth", () => {
    expect(parseQuantity("1Ti")).toBe(1024 ** 4)
  })

  it("parses Pi as 1024 to the fifth", () => {
    expect(parseQuantity("1Pi")).toBe(1024 ** 5)
  })

  it("parses Ei as 1024 to the sixth", () => {
    expect(parseQuantity("1Ei")).toBe(1024 ** 6)
  })

  it("parses decimal k suffix as 1000", () => {
    expect(parseQuantity("1k")).toBe(1000)
  })

  it("parses decimal M suffix as 1000 squared", () => {
    expect(parseQuantity("1M")).toBe(1_000_000)
  })

  it("parses decimal G suffix as 1000 cubed", () => {
    expect(parseQuantity("1G")).toBe(1_000_000_000)
  })

  it("parses nano suffix — metrics-server reports CPU usage in nanocores", () => {
    expect(parseQuantity("2785315627n")).toBeCloseTo(2.785315627, 6)
  })

  it("parses micro suffix as a millionth", () => {
    expect(parseQuantity("500u")).toBe(500 / 1e6)
  })

  it("parses zero nanocores", () => {
    expect(parseQuantity("0n")).toBe(0)
  })

  it("parses a bare integer", () => {
    expect(parseQuantity("42")).toBe(42)
  })

  it("parses a bare decimal", () => {
    expect(parseQuantity("1.5")).toBe(1.5)
  })

  it("parses a fractional Gi value", () => {
    expect(parseQuantity("1.5Gi")).toBe(1.5 * 1024 ** 3)
  })

  it("falls back to 0 for unparseable input", () => {
    expect(parseQuantity("abc")).toBe(0)
  })

  it("returns 0 for a bare suffix instead of poisoning totals with NaN", () => {
    // A malformed quantity (just a suffix, no number) must not propagate NaN
    // into the aggregated totals and UI percentages.
    expect(parseQuantity("m")).toBe(0)
    expect(parseQuantity("Gi")).toBe(0)
    expect(parseQuantity("Ki")).toBe(0)
  })

  it("parses zero", () => {
    expect(parseQuantity("0")).toBe(0)
  })

  it("parses zero with a suffix", () => {
    expect(parseQuantity("0Gi")).toBe(0)
  })
})

describe("humanizeBytes", () => {
  it("formats sub-kilobyte values with a B suffix", () => {
    expect(humanizeBytes(0)).toBe("0B")
    expect(humanizeBytes(1023)).toBe("1023B")
  })

  it("formats megabytes as Mi without decimals", () => {
    expect(humanizeBytes(1024 ** 2)).toBe("1Mi")
  })

  it("formats gigabytes as Gi with one decimal", () => {
    expect(humanizeBytes(1.5 * 1024 ** 3)).toBe("1.5Gi")
  })

  it("formats terabytes as Ti with one decimal", () => {
    expect(humanizeBytes(1024 ** 4)).toBe("1.0Ti")
  })
})

describe("humanizeCpu", () => {
  it("formats zero as 0m", () => {
    expect(humanizeCpu(0)).toBe("0m")
  })

  it("formats half a core as 500m", () => {
    expect(humanizeCpu(0.5)).toBe("500m")
  })

  it("formats an integer core count without decimals", () => {
    expect(humanizeCpu(1)).toBe("1")
    expect(humanizeCpu(2)).toBe("2")
  })

  it("formats a non-integer core count with two decimals", () => {
    expect(humanizeCpu(1.5)).toBe("1.50")
  })
})
