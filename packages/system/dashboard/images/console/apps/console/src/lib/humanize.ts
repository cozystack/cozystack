/**
 * Insert spaces between camel-cased words but keep consecutive capital
 * acronyms together: `VMInstance` → `VM Instance`, `HTTPCache` → `HTTP Cache`,
 * `ClickHouse` → `ClickHouse` (unchanged), `OpenBAO` → `Open BAO`.
 */
export function humanizeKind(kind: string): string {
  return kind
    .replace(/([a-z])([A-Z])/g, "$1 $2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
    .replace(/\s+/g, " ")
    .trim()
}
