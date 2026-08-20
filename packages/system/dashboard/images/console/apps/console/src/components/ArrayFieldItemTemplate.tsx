import type {
  ArrayFieldTemplateItemType,
  FormContextType,
  RJSFSchema,
  StrictRJSFSchema,
} from "@rjsf/utils"

/**
 * One row of an RJSF array field: the item itself plus a hover-revealed
 * remove control. Numeric items get a fixed narrow column so a list of
 * numbers doesn't stretch across the form.
 */
export function ArrayFieldItemTemplate<
  T = unknown,
  S extends StrictRJSFSchema = RJSFSchema,
  F extends FormContextType = FormContextType,
>(props: ArrayFieldTemplateItemType<T, S, F>) {
  const { children, hasRemove, index, onDropIndexClick, disabled, readonly, schema } = props
  const isNumeric = schema.type === 'integer' || schema.type === 'number'
  return (
    <div className="array-item-row group flex items-center gap-1.5 mb-1">
      <div className={isNumeric ? 'w-36 shrink-0' : 'flex-1'}>{children}</div>
      {hasRemove && (
        <button
          type="button"
          aria-label="Remove"
          className="opacity-0 group-hover:opacity-100 size-[26px] flex-shrink-0 flex items-center justify-center rounded-full border border-red-200 bg-white text-red-400 text-sm leading-none hover:bg-red-50 hover:text-red-600 hover:border-red-300 disabled:opacity-30 transition-all duration-150"
          onClick={onDropIndexClick(index)}
          disabled={disabled || readonly}
        >
          ×
        </button>
      )}
    </div>
  )
}
