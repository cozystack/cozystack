import type { RJSFValidationError } from "@rjsf/utils"

/**
 * RJSF builds `property` as the JSON-pointer instance path with the slashes
 * swapped for dots (`processRawValidationErrors.js`), so the path is always
 * dotted and never bracketed -- an array index arrives as `.items.0.name`.
 *
 * The flattening is lossy for a property whose own name contains a dot, which
 * an additionalProperties map keyed `ghcr.io` produces: the split yields one
 * segment too many and the generated id does not match the rendered one, so
 * that field is not brought into view. Pinned in the tests.
 */
function propertySegments(property: string): string[] {
  return property.split(".").filter((segment) => segment !== "")
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
  const id = [idPrefix, ...propertySegments(error.property ?? "")].join(idSeparator)
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
  const field =
    document.getElementById(id) ?? document.querySelector<HTMLElement>(fallback)
  field?.scrollIntoView?.({ block: "center" })
  field?.focus?.({ preventScroll: true })
}
