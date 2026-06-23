/**
 * Parse a Kubernetes resource.Quantity string into a numeric value in
 * the canonical units (cores for CPU, bytes for memory). Extracted from
 * the QuotaDisplay helpers; nano/micro (n/u) support was added on top for
 * the nanocore CPU values metrics-server reports. See the test file for
 * the pinned edge cases.
 */
export function parseQuantity(s: string): number {
  if (!s) return 0
  // A malformed quantity (e.g. a bare suffix like "m") parses to NaN, which
  // would poison every total and percentage it feeds into. Treat it as 0.
  const n = parseFloat(s)
  if (!Number.isFinite(n)) return 0
  if (s.endsWith("m")) return n / 1000
  // Decimal SI sub-units — metrics-server reports CPU usage in nanocores
  if (s.endsWith("n")) return n / 1e9
  if (s.endsWith("u")) return n / 1e6
  // Binary SI suffixes (powers of 1024)
  if (s.endsWith("Ki")) return n * 1024
  if (s.endsWith("Mi")) return n * 1024 ** 2
  if (s.endsWith("Gi")) return n * 1024 ** 3
  if (s.endsWith("Ti")) return n * 1024 ** 4
  if (s.endsWith("Pi")) return n * 1024 ** 5
  if (s.endsWith("Ei")) return n * 1024 ** 6
  // Decimal SI suffixes (powers of 1000) — Kubernetes uses lowercase k
  if (s.endsWith("k")) return n * 1000
  if (s.endsWith("M")) return n * 1000 ** 2
  if (s.endsWith("G")) return n * 1000 ** 3
  return n
}

export function humanizeBytes(bytes: number): string {
  if (bytes >= 1024 ** 4) return `${(bytes / 1024 ** 4).toFixed(1)}Ti`
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)}Gi`
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(0)}Mi`
  return `${bytes}B`
}

export function humanizeCpu(val: number): string {
  if (val < 1) return `${Math.round(val * 1000)}m`
  return `${val % 1 === 0 ? val : val.toFixed(2)}`
}
