import type { RJSFValidationError } from "@rjsf/utils"

function propertySegments(property: string): string[] {
  const segments: string[] = []
  const pattern = /(?:^|\.)([^.[\]]+)|\[(?:'([^']*)'|"([^"]*)"|([^\]]+))\]/g
  for (const match of property.matchAll(pattern)) {
    const segment = match[1] ?? match[2] ?? match[3] ?? match[4]
    if (segment) segments.push(segment)
  }
  return segments
}

/**
 * Errors render inline with the error list hidden, so a blocked submit is
 * invisible unless the offending field is brought into view. RJSF's built-in
 * focus resolves the field through `form.elements`, which the `tagName="div"`
 * form does not have, so resolve it by generated id instead.
 *
 * Not every widget puts that id on an element. `SourceField` and
 * `SourceWidget` render radios carrying only `name="<id>-source"`, so an exact
 * lookup finds nothing and the submit scrolls nowhere. Fall back to the first
 * input whose id or name starts with the generated id.
 */
export function focusFirstError(
  error: Pick<RJSFValidationError, "property">,
  idPrefix = "root",
  idSeparator = "_",
) {
  const id = [idPrefix, ...propertySegments(error.property ?? "")].join(idSeparator)
  const escaped = id.replace(/["\\]/g, "\\$&")
  const field =
    document.getElementById(id) ??
    document.querySelector<HTMLElement>(
      `input[id^="${escaped}"], input[name^="${escaped}"]`,
    )
  field?.scrollIntoView?.({ block: "center" })
  field?.focus?.({ preventScroll: true })
}
