import type { RJSFValidationError } from "@rjsf/utils"

/**
 * Errors render inline with the error list hidden, so a blocked submit is
 * invisible unless the offending field is brought into view. RJSF's built-in
 * focus resolves the field through `form.elements`, which the `tagName="div"`
 * form does not have, so resolve it by generated id instead.
 *
 * Not every widget puts that id on an element. `SourceField` and
 * `SourceWidget` render radios carrying only `name="<id>-source"`, so an exact
 * lookup finds nothing and the submit scrolls nowhere. Fall back to the first
 * element whose id or name starts with the generated id.
 */
export function focusFirstError(error: Pick<RJSFValidationError, "property">) {
  const segments = (error.property ?? "")
    .replace(/\['?([^'\]]+)'?\]/g, ".$1")
    .split(".")
    .filter(Boolean)
  const id = ["root", ...segments].join("_")
  const escaped = id.replace(/["\\]/g, "\\$&")
  const field =
    document.getElementById(id) ??
    document.querySelector<HTMLElement>(`[id^="${escaped}"], [name^="${escaped}"]`)
  field?.scrollIntoView?.({ block: "center" })
  field?.focus?.({ preventScroll: true })
}
