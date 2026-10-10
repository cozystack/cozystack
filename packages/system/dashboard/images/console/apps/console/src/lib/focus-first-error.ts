import type { RJSFValidationError } from "@rjsf/utils"

/**
 * RJSF builds `property` as the JSON-pointer instance path with the slashes
 * swapped for dots (`processRawValidationErrors.js`), so the path is always
 * dotted and never bracketed -- an array index arrives as `.items.0.name`.
 */
function propertySegments(property: string): string[] {
  return property.split(".").filter((segment) => segment !== "")
}

/**
 * The flattening is lossy: a dot inside one property name is indistinguishable
 * from the separator between two, and a map keyed `john.doe` or `ghcr.io`
 * produces exactly that. So try the plain split first, then every way of
 * gluing adjacent segments back together with the dot they may have come from.
 * Bounded, because the count doubles with each segment and only the first
 * matching id is used.
 */
function candidateIds(
  segments: string[],
  idPrefix: string,
  idSeparator: string,
): string[] {
  const ids: string[] = []
  const build = (index: number, parts: string[]) => {
    if (ids.length >= 64) return
    if (index === segments.length) {
      ids.push([idPrefix, ...parts].join(idSeparator))
      return
    }
    build(index + 1, [...parts, segments[index]])
    if (parts.length > 0) {
      const glued = `${parts[parts.length - 1]}.${segments[index]}`
      build(index + 1, [...parts.slice(0, -1), glued])
    }
  }
  build(0, [])
  return ids
}

/**
 * Errors render inline with the error list hidden, so a blocked submit is
 * invisible unless the offending field is brought into view. RJSF's built-in
 * focus resolves the field through `form.elements`, which the `tagName="div"`
 * form does not have, so resolve it by generated id instead.
 *
 * Not every widget puts that id on a control, so fall back to a descendant,
 * matched on a segment boundary: a bare prefix would let an error on `data`
 * grab the sibling `database`, scrolling to the wrong field. The `name` half
 * catches a radio group, which carries `name="<id>-source"` and no id.
 *
 * A widget that renders neither is unreachable, which is why the widgets this
 * console binds set the generated id themselves rather than relying on this.
 */
export function focusFirstError(
  error: Pick<RJSFValidationError, "property">,
  idPrefix = "root",
  idSeparator = "_",
) {
  const candidates = candidateIds(
    propertySegments(error.property ?? ""),
    idPrefix,
    idSeparator,
  )
  const id = candidates[0]
  const quote = (v: string) => v.replace(/["\\]/g, "\\$&")
  const escaped = quote(id)
  const escapedSeparator = quote(idSeparator)
  // Not every widget puts the id on a form control, so match any of them
  // rather than inputs alone.
  const controls = ["input", "select", "textarea"]
  const fallback = [
    ...controls.map((tag) => `${tag}[id^="${escaped}${escapedSeparator}"]`),
    ...controls.map((tag) => `${tag}[name^="${escaped}-"]`),
  ].join(", ")
  // An exact id is unambiguous, so try every candidate before falling back to
  // the heuristic descendant match, which only makes sense for the plain split.
  const exact = candidates
    .map((candidate) => document.getElementById(candidate))
    .find((element): element is HTMLElement => element !== null)
  const field = exact ?? document.querySelector<HTMLElement>(fallback)
  field?.scrollIntoView?.({ block: "center" })
  field?.focus?.({ preventScroll: true })
}
