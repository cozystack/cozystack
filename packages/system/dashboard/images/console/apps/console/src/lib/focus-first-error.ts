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
 * Not every widget puts that id on an element. `SourceField` and
 * `SourceWidget` render radios carrying only `name="<id>-source"`, so an exact
 * lookup finds nothing and the submit scrolls nowhere. Fall back to a
 * descendant input, matched on a segment boundary: a bare prefix would let an
 * error on `data` grab the sibling `database`, scrolling to the wrong field.
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
  const field =
    document.getElementById(id) ??
    document.querySelector<HTMLElement>(
      `input[id^="${escaped}${escapedSeparator}"], input[name^="${escaped}-"]`,
    )
  field?.scrollIntoView?.({ block: "center" })
  field?.focus?.({ preventScroll: true })
}
